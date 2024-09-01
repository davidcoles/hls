package adts

// https://wiki.multimedia.cx/index.php/ADTS

// AAAAAAAA AAAABCCD EEFFFFGH HHIJKLMM MMMMMMMM MMMOOOOO OOOOOOPP (QQQQQQQQ QQQQQQQQ)

type Frame []byte

func ADTS() func([]byte, func([]byte, bool) bool) bool {
	var raw [65536]byte
	max := 2000

	pos := 0
	off := 0
	frameLength := 0

	return func(buff []byte, fx func([]byte, bool) bool) bool {

		for n := 0; n < len(buff); n++ {
			b := buff[n]

			raw[off] = b
			off++

			if off > max {
				//fmt.Println("adts unsync too big")
				d := make([]byte, off)
				copy(d[:], raw[0:off])
				fx(d, false)
				pos = 0
				off = 0
				continue
			}

			switch pos {
			case 0: // AAAAAAAA
				if b != 0xff {
					pos = 0
					continue
				}
			case 1: // AAAABCCD
				if b&0xf0 != 0xf0 {
					pos = 0
					continue
				}
				if b&0x6 != 0 { // C - layer: always 0
					pos = 0
					continue
				}
			case 2: // EEFFFFGH
			case 3: // HHIJKLMM
				frameLength = int(b&3) << 11
			case 4: // MMMMMMMM
				frameLength += int(b) << 3
			case 5: // MMMOOOOO
				frameLength += int(b&224) >> 5
				if frameLength > max {
					pos = 0
					continue
				}
			case 6: // OOOOOOPP
			case 7: // optional: (QQQQQQQQ
			case 8: // optional: QQQQQQQQ)
			}

			pos++

			if pos > 8 && pos == frameLength {
				pre := off - frameLength

				if pre > 0 {
					d := make([]byte, pre)
					copy(d[:], raw[0:pre])
					fx(d, false)
				}

				d := make([]byte, frameLength)
				copy(d[:], raw[pre:pre+frameLength])
				fx(d, true)
				pos = 0
				off = 0
			}
		}

		return true
	}
}

// A 12 syncword 0xFFF, all bits must be 1
func (f Frame) Syncword() int {
	return (int(f[0]) << 4) + (int(f[1]) >> 4)
}

// B 1 MPEG Version: 0 for MPEG-4, 1 for MPEG-2
func (f Frame) MpegVersion() int {
	return int(f[1]&8) >> 3
}

// C 2 Layer: always 0
func (f Frame) Layer() int {
	return int(f[1]&6) >> 1
}

// D 1 protection absent, Warning, set to 1 if there is no CRC and 0 if there is CRC
func (f Frame) ProtectionAbsent() int {
	return int(f[1] & 1)
}

// E 2 profile, the MPEG-4 Audio Object Type minus 1
func (f Frame) Profile() int {
	return int(f[2]&192) >> 6
}

// F 4 MPEG-4 Sampling Frequency Index (15 is forbidden)
func (f Frame) SamplingFrequencyIndex() int {
	// EEFFFFGH
	return int(f[2]&60) >> 2
}

// G 1 private bit, guaranteed never to be used by MPEG, set to 0 when encoding, ignore when decoding
func (f Frame) Private() int {
	return int(f[2]&2) >> 1
}

// H 3 MPEG-4 Channel Configuration (when 0, the channel configuration is sent via an inband PCE)
func (f Frame) ChannelConfiguration() int {
	return (int(f[2]&1) << 2) + (int(f[3]&192) >> 6)
}

// I 1 originality, set to 0 when encoding, ignore when decoding
func (f Frame) Originality() int {
	return int(f[3]&32) >> 5
}

// J 1 home, set to 0 when encoding, ignore when decoding
func (f Frame) Home() int {
	return int(f[3]&16) >> 4
}

// K 1 copyrighted id bit, the next bit of a centrally registered
// copyright identifier, set to 0 when encoding, ignore when decoding
func (f Frame) CopyrightedIdBit() int {
	return int(f[3]&8) >> 3
}

// L 1 copyright id start, signals that this frame's copyright id bit is
// the first bit of the copyright id, set to 0 when encoding, ignore
// when decoding
func (f Frame) CopyrightedIdStart() int {
	return int(f[3]&4) >> 2
}

// M 13 frame length, this value must include 7 or 9 bytes of header
// length: FrameLength = (ProtectionAbsent == 1 ? 7 : 9) +
// size(AACFrame)
func (f Frame) FrameLength() int {
	return (int(f[3]&3) << 11) + (int(f[4]) << 3) + (int(f[5]&224) >> 5)
}

// O 11 Buffer fullness
func (f Frame) BufferFullness() int {
	return (int(f[5]&31) << 6) + (int(f[6]&252) >> 2)
}

// P 2 Number of AAC frames (RDBs) in ADTS frame minus 1, for maximum
// compatibility always use 1 AAC frame per ADTS frame
func (f Frame) NumberAACFramesMinusOne() int {
	return int(f[6] & 3)
}

// Q 16 CRC if protection absent is 0
func (f Frame) CRC() uint16 {
	return uint16(f[7])<<8 | uint16(f[8])
}

func (f Frame) HeaderLength() int {
	// signature: 0xff, 0xf1, 0x3c
	if f.ProtectionAbsent() == 0 {
		return 9
	}
	return 7
}

func (f Frame) AACFrame() []byte {
	// signature: 0xff, 0xf1, 0x3c
	start := f.HeaderLength()
	//end := start + f.FrameLength()
	return f[start:]
}

func (f Frame) SamplingFrequency() uint {
	switch f.SamplingFrequencyIndex() {
	case 0:
		return 96000
	case 1:
		return 88200
	case 2:
		return 64000
	case 3:
		return 48000
	case 4:
		return 44100
	case 5:
		return 32000
	case 6:
		return 24000
	case 7:
		return 22050
	case 8:
		return 16000
	case 9:
		return 12000
	case 10:
		return 11025
	case 11:
		return 8000
	case 12:
		return 7350
	}
	return 0
}

/**********************************************************************
  Derived properties
**********************************************************************/

const nano = 1000000000

func (f Frame) FramesPerSecond() float64 {
	return float64(f.SamplingFrequency()) / float64(1024) // 1024 samples in a frame
}

func (f Frame) FrameLengthNano() uint64 {
	return uint64(float64(nano) / f.FramesPerSecond()) // length of a single ADTS frame in nanoseconds
}
