package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"hls/adts"
	"hls/mpegts"
)

// values used are taken from an example working server (Wowza, if I recall correctly)
const PROGRAM_NUMBER = 1
const PROGRAM_MAP_PID = 4095
const STREAM_PACKET_IDENTIFIER = 257
const STREAM_ID = 192
const ESD_PID = 258

type packet = [188]byte

type chunk struct {
	index    uint64
	duration uint64
	data     []byte
}

func main() {

	redirect := flag.String("r", "", "redirect url for non-existent pages")
	minimum := flag.Uint("m", 0, "minimum number of active streams required for server to be deemed healthy")

	flag.Parse()

	args := flag.Args()

	addr := args[0]
	base := args[1]
	list := args[2:]

	directory := startdirectory(base, list)
	server(addr, directory, *redirect, *minimum)
}

type directory struct {
	mutex   sync.Mutex
	streams map[string]*stream
}

func startdirectory(base string, streams []string) *directory {
	d := &directory{streams: map[string]*stream{}}

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {

			d.mutex.Lock()
			for _, s := range streams {
				if _, ok := d.streams[s]; !ok {
					d.streams[s] = start(base, s)
				}
			}

			for k, v := range d.streams {
				select {
				default:
				case <-v.done:
					delete(d.streams, k)
				}
			}
			d.mutex.Unlock()

			select {
			case <-ticker.C:
			}
		}
	}()

	return d
}

func (d *directory) find(s string) *stream {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.streams[s]
}

func (d *directory) list() (list []string) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	for k, v := range d.streams {
		if v.ok() {
			list = append(list, k)
		}
	}
	return
}

type stream struct {
	mutex sync.Mutex
	list  []chunk
	done  chan bool
}

func (s *stream) bandwidth() uint {
	return 52850
}

func (s *stream) chunk(i uint64) []byte {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	for _, c := range s.list {
		if c.index == i {
			return c.data
		}
	}
	return nil
}

func (s *stream) ok() bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return len(s.list) > 2
}

func (s *stream) index() (list [][2]uint64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if len(s.list) < 3 {
		return
	}

	for _, c := range s.list {
		list = append(list, [2]uint64{c.index, c.duration})
	}

	for len(list) > 3 {
		list = list[1:]
	}

	return
}

func start(base, name string) (s *stream) {

	s = &stream{done: make(chan bool)}

	go func() {
		url := base + "/" + name
		defer func() {
			time.Sleep(20 * time.Second) // delay before retrying
			close(s.done)
		}()

		in := open(url)

		if in == nil {
			return
		}

		index := uint64(time.Now().Unix()) / 10
		adjust := 3120 * uint64(time.Second)
		a2m := adtsToMPEGTS(uint64(time.Now().UnixNano())+adjust, 10, true)
		data := make([]byte, 0, 100000)

		for c := range in {
			out, err := a2m(c)

			if err != nil {
				s.mutex.Lock()
				s.list = nil
				s.mutex.Unlock()
				return
			}

			for _, o := range out {
				if ts := mpegts.TS(o); ts.TEI() {
					//log.Printf("Chunk %d for %s, %d bytes\n", index, name, len(data))

					if len(data) > 0 {

						s.mutex.Lock()
						s.list = append(s.list, chunk{index: index, duration: marker(o).duration(), data: data})
						for len(s.list) > 10 {
							s.list = s.list[1:]
						}
						s.mutex.Unlock()
						index++
					}

					data = make([]byte, 0, 100000)
				} else {
					data = append(data, o[:]...)
				}
			}
		}
	}()

	return
}

type marker [188]byte

func dummy(timestamp, duration uint64) (dummy marker) {
	dummy[1] = 0x80 //set TEI bit
	binary.BigEndian.PutUint64(dummy[2:], timestamp)
	binary.BigEndian.PutUint64(dummy[2+8:], duration)
	return
}

func (m marker) timestamp() uint64 {
	return binary.BigEndian.Uint64(m[2:])
}

func (m marker) duration() uint64 {
	return binary.BigEndian.Uint64(m[2+8:])
}

func (m marker) time() time.Time {
	ts := m.timestamp()
	sec := ts / uint64(time.Second)
	ns := ts % uint64(time.Second)
	return time.Unix(int64(sec), int64(ns))
}

func adtsProgramSpecificInformation() func() []packet {
	var program_number uint16 = PROGRAM_NUMBER
	var program_map_pid uint16 = PROGRAM_MAP_PID
	var pid uint16 = STREAM_PACKET_IDENTIFIER
	var esdpid uint16 = ESD_PID
	//esd := []byte{14,3,192,3,32} ???
	//	NAK & \r \255 \255 I D 3 ' ' 255 ID 3 ' ' \0 SI
	// program descriptors and elementary stream info data - just taken from a working server
	dsc := []byte{0x25, 255, 255, 73, 68, 51, 32, 255, 73, 68, 51, 32, 0, 3, 0, 1}
	esd := []byte{0x15, 38, 13, 255, 255, 73, 68, 51, 32, 255, 73, 68, 51, 32, 0, 15}
	return mpegts.ProgramSpecificInformation(program_number, program_map_pid, pid, dsc, esd, esdpid)
}

func adtsToMPEGTS(start uint64, interval uint, marker bool) func(adts.Frame) ([]packet, error) {

	patpmt := adtsProgramSpecificInformation()
	pes := packetizedElementaryStream(STREAM_PACKET_IDENTIFIER, STREAM_ID)

	var totalFramesProcessed uint64
	var framesSincePAT uint
	var fps float64
	var sfq uint
	var tic uint64

	return func(frame adts.Frame) (out []packet, err error) {

		if sfq == 0 {
			sfq = frame.SamplingFrequency()
			if sfq == 0 {
				return nil, fmt.Errorf("SamplingFrequency is zero")
			}

			fps = frame.FramesPerSecond()
			tic = frame.FrameLengthNano()
		}

		if frame.SamplingFrequency() != sfq {
			return nil, fmt.Errorf("SamplingFrequency changed")
		}

		if frame.NumberAACFramesMinusOne() != 0 {
			return nil, fmt.Errorf("NumberAACFrames is greater than one")
		}

		// if we have more than (approximately) <interval> seconds of audio then reset count to trigger PAT/PMT
		if framesSincePAT > (uint(fps) * interval) {

			// and also send a dummy packet (if requested) with some metadata about the chunk which has just finished
			if marker {
				out = append(out, dummy(start+tic*totalFramesProcessed, uint64(framesSincePAT)*tic)) // timestamp, duration
			}

			framesSincePAT = 0
		}

		// will send a PAT/PMT at the start and every <interval> seconds thereafter
		if framesSincePAT == 0 {
			out = append(out, patpmt()...)
		}

		// we kep things simple and send one audio frame per PES packet (which can span multiple 188-byte network packets)
		out = append(out, pes(frame, start+tic*totalFramesProcessed)...)

		totalFramesProcessed++
		framesSincePAT++

		return
	}
}

func stdin(out chan adts.Frame) {
	f := os.Stdin
	defer close(out)

	fn := adts.ADTS()

	for {
		buff := make([]byte, 8192)
		n, err := f.Read(buff)
		if err != nil {
			return
		}

		fn(buff[:n], func(frame []byte, sync bool) bool {
			if !sync {
				panic("sync")
			}
			out <- frame
			return true
		})
	}
}

// could probably go into MPEG-TS library
func packetizedElementaryStream(packetID uint16, streamID uint8) func([]byte, uint64) []packet {
	cc := uint8(0) // increments with each invocation of the returned closure
	//dai := true
	//sid := uint8(STREAM_ID)
	//pid := uint16(STREAM_PACKET_IDENTIFIER)

	return func(data []byte, time uint64) (out []packet) {

		pcr := mpegts.Nano90KHz(time)
		oph := mpegts.OptionalPESHeader(make([]byte, 8), true, pcr) // 'true' is DAI (Data alignment indicator) - could be an arg
		pes := mpegts.PESPacket(streamID, oph, data)
		siz := uint(176)

		if len(pes) < 176 {
			siz = uint(len(pes))
		}
		af := mpegts.AdaptationField(make([]byte, 184-siz), false, true, false, mpegts.AFPCR(pcr))
		out = append(out, mpegts.TransportStreamPacket(true, false, packetID, cc%16, af, pes[0:siz]))
		pes = pes[siz:]
		cc++

		for len(pes) >= 184 {
			out = append(out, mpegts.TransportStreamPacket(false, false, packetID, cc%16, nil, pes[:184]))
			pes = pes[184:]
			cc++
		}

		if len(pes) > 0 {
			out = append(out, mpegts.TransportStreamPacket(false, false, packetID, cc%16, nil, pes))
			cc++
		}

		return
	}
}

func open(endpoint string) chan []byte {

	headers := map[string]string{}

	client := &http.Client{
		//CheckRedirect: redirectPolicyFunc,
	}

	req, err := http.NewRequest("GET", endpoint, nil)
	//req.Header.Add("Icy-MetaData", "1")
	resp, err := client.Do(req)

	if err != nil {
		//log.Println(endpoint, err)
		return nil
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil
	}

	for k, _ := range resp.Header {
		if len(k) < 5 {
			continue
		}

		switch k {
		case "Content-Type":
			headers[k] = resp.Header[k][0]
		default:
			if k[0:4] == "Icy-" || k[0:4] == "Ice-" {
				headers[k] = resp.Header[k][0]
			}
		}
	}

	ch := make(chan []byte)

	metaint := uint(0)

	if ice_metadata, ok := headers["Icy-Metaint"]; ok {
		if p, err := strconv.Atoi(ice_metadata); err != nil || p < 0 {
			//log.Println("icy-metaint must be a positive integer", ice_metadata)
			resp.Body.Close()
			return nil
		} else {
			metaint = uint(p)
		}
	}

	demux := demuxmeta(metaint)

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		chunk := 8192

		fn := adts.ADTS()

		for {
			buff := make([]byte, chunk)

			if nread, _ := io.ReadFull(resp.Body, buff); nread != chunk {
				//log.Println(endpoint, "short read", nread, chunk)
				return
			}

			cromulent := true

			demux(buff, func(b []byte, m bool) {
				if m {
					//log.Println(string(b))
				} else {
					fn(b, func(frame []byte, sync bool) bool {
						if cromulent && sync {
							ch <- frame
							return true
						}
						cromulent = false
						return false
					})
				}
			})

			if !cromulent {
				return
			}
		}
	}()

	return ch

}

func demuxmeta(mint uint) func([]byte, func([]byte, bool)) {

	// handle degenerate case
	if mint == 0 {
		return func(buff []byte, f func([]byte, bool)) {
			f(buff, false)
		}
	}

	stat := 0               // state: 0 - data, 1 - metaint byte, 2 - metadata
	todo := mint            // remaining bytes for this state
	meta := make([]byte, 0) // metadata buffer

	return func(buff []byte, f func([]byte, bool)) {
		for len(buff) > 0 {
			switch stat {
			case 0: // not in metadata
				if uint(len(buff)) < todo {
					f(buff, false)
					todo -= uint(len(buff))
					return
				}
				f(buff[:todo], false)
				buff = buff[todo:] // may be empty - caught by for{} condition
				todo = 0
				stat = 1

			case 1: // read metalen byte
				todo = uint(buff[0]) << 4    // * 16
				meta = make([]byte, 0, 4096) // greater then 255*16
				buff = buff[1:]
				stat = 2

			case 2: // in metadata
				if uint(len(buff)) < todo {
					meta = append(meta, buff[:]...)
					todo -= uint(len(buff))
					return
				}
				meta := append(meta, buff[:todo]...)
				f(meta, true)
				buff = buff[todo:]
				todo = mint
				stat = 0
			}
		}
	}
}

func server(addr string, directory *directory, redirect string, minimum uint) {

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "Anarcast")

		if r.URL.Path == "/" {
			if redirect != "" {
				http.Redirect(w, r, redirect, http.StatusSeeOther)
			} else {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("Hello, World!\n"))
			}
			return
		}

		if r.URL.Path == "/healthy" {
			if len(directory.list()) < int(minimum) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}

		re := regexp.MustCompile(`^/([A-Za-z0-9.-_]+)/(|playlist.m3u8|chunklist.m3u8|(\d+).ts)$`)
		match := re.FindStringSubmatch(r.URL.Path)

		if len(match) != 4 {
			if redirect != "" {
				http.Redirect(w, r, redirect, http.StatusSeeOther)
			} else {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte("Sorry\n"))
			}

			return
		}

		mountpoint := match[1]
		stream := directory.find(mountpoint)

		if stream == nil || stream.index() == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		//log.Println(mountpoint, match[3])
		switch match[2] {

		case "": // eg.: http://hls.example.com/streamname/ - so send the playlist
			fallthrough
		case "playlist.m3u8":

			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.Header().Set("Transfer-Encoding", "chunked")
			w.Header().Set("Connection", "keep-alive")

			fmt.Fprintf(w, "#EXTM3U\n")
			fmt.Fprintf(w, "#EXT-X-VERSION:3\n")
			fmt.Fprintf(w, "#EXT-X-STREAM-INF:PROGRAM-ID=1,BANDWIDTH=%d,CODECS=\"mp4a.40.2\"\n", stream.bandwidth())
			fmt.Fprintf(w, "chunklist.m3u8\n")

		case "chunklist.m3u8":

			list := stream.index()

			if len(list) < 3 {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, GET, POST, HEAD")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Expose-Headers", "Date, Server, Content-Type, Content-Length")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, User-Agent, If-Modified-Since, Cache-Control, Range")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			w.WriteHeader(http.StatusOK)

			fmt.Fprintln(w, "#EXTM3U")
			fmt.Fprintln(w, "#EXT-X-VERSION:3")
			fmt.Fprintln(w, "#EXT-X-TARGETDURATION:12") // >= max segment length
			fmt.Fprintln(w, "#EXT-X-MEDIA-SEQUENCE: ", list[0][0])

			for _, v := range list {
				fmt.Fprintf(w, "#EXTINF:%.2f\n%d.ts\n", float64(v[1])/1000000000, v[0])
			}

		default:

			index, err := strconv.Atoi(match[3])

			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			chunk := stream.chunk(uint64(index))

			if len(chunk) < 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}

			w.Header().Set("Access-Control-Expose-Headers", "Date, Server, Content-Type, Content-Length")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, GET, POST, HEAD")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, User-Agent, If-Modified-Since, Cache-Control, Range")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(chunk)))
			w.Header().Set("Content-Type", "video/MP2T")
			w.WriteHeader(http.StatusOK)
			w.Write(chunk)
		}
	})

	log.Fatal(http.ListenAndServe(addr, nil))

}
