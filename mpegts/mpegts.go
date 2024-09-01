package mpegts

import (
// "log"
)

const (
	TID_PROGRAM_ASSOCIATION          = 0x00
	TID_CONDITIONAL_ACCESS           = 0x01
	TID_PROGRAM_MAP                  = 0x02
	TID_TRANSPORT_STREAM_DESCRIPTION = 0x03

	MPEGTS_PACKET_LENGTH = 188
)

func Nano90KHz(n uint64) uint64 {
	return (n * 90000) / 1000000000
}

func ProgramAssociationTable(program_number uint16, program_map_pid uint16, cc uint8) [188]byte {
	pat_data := make(map[uint16]uint16)
	pat_data[program_number] = program_map_pid
	return TransportStreamPacket(true, false, 0, cc, nil, PAT_Payload(program_number, pat_data))
}

// func TransportStreamPacket(pusi bool, tp bool, pid uint16, cc uint8, af []byte, pd []byte) [188]byte {
func PAT_Payload(program_number uint16, pat_data map[uint16]uint16) []byte {
	var payload [184]byte
	for n := 0; n < len(payload); n++ {
		payload[n] = 0xff
	}

	syntax_section := TableSyntaxSection(program_number, PAT_Table(pat_data))
	table_header := TableHeader(TID_PROGRAM_ASSOCIATION, syntax_section)

	payload[0] = 0
	copy(payload[1:], table_header[:])
	return payload[:]
}

func PAT_Table(pat map[uint16]uint16) []byte {
	t := make([]byte, len(pat)*4)
	n := 0
	for pn, pmpid := range pat {
		t[n*4+0] = byte(pn >> 8 & 0xff)
		t[n*4+1] = byte(pn & 0xff)
		t[n*4+2] = byte(pmpid>>8) | 0xe0
		t[n*4+3] = byte(pmpid & 0xff)
		n++
	}
	return t
}

func ProgramMappingTable(p uint16, pmpid uint16, cc uint8, pcrpid uint16, desc []byte, pd map[uint16][]byte) [188]byte {
	return TransportStreamPacket(true, false, pmpid, cc, nil, PMT_Payload(p, pcrpid, desc, pd))
}

func PMT_Payload(tide uint16, pcrpid uint16, desc []byte, pmtd map[uint16][]byte) []byte {
	var payload [184]byte
	for n := 0; n < len(payload); n++ {
		payload[n] = 0xff
	}

	syntax_section := TableSyntaxSection(tide, PMT_Table(pcrpid, desc, pmtd))
	table_header := TableHeader(TID_PROGRAM_MAP, syntax_section)

	payload[0] = 0
	copy(payload[1:], table_header[:])
	return payload[:]
}

func PMT_Table(pcrpid uint16, desc []byte, pmtd map[uint16][]byte) []byte {
	essd := make([]byte, 0, 188)

	pids := []uint16{258, 257}
	for _, pid := range pids {
		esd := pmtd[pid]

		//	for pid, esd := range pmtd {
		est := esd[0]
		esd = esd[1:]

		e := make([]byte, 5+len(esd))
		esill := len(esd)

		e[0] = est

		e[1] = (0x07 << 5) | byte((pid>>8)&0x1f) // rb(0x07) & pid-hi
		e[2] = byte(pid & 0xff)                  // pid-lo
		e[3] = (0x0f << 4) | (0x0 << 2) | byte((esill>>8)&0x03)
		e[4] = byte(esill & 0xff)
		copy(e[5:], esd[:])
		essd = append(essd, e[:]...)
		//log.Println(len(e))
	}

	pt := desc[0]
	desc = desc[1:]
	pd := make([]byte, 2+len(desc))
	pd[0] = pt //metadata pointer
	pd[1] = byte(len(desc))

	copy(pd[2:], desc[:])

	pmt := make([]byte, 4+len(pd)+len(essd))

	pmt[0] = (0x07 << 5) | byte((pcrpid>>8)&0x1f) // rb(0x07)
	pmt[1] = byte(pcrpid & 0xff)
	pmt[2] = (0x0f << 4) | (0x00 << 2) | byte((len(pd)>>8)&0x03)
	pmt[3] = byte(len(pd) & 0xff)

	copy(pmt[4:], pd[:])
	copy(pmt[4+len(pd):], essd)

	//log.Println(len(pmt))
	return pmt
}

func AdtsPatPmt() func(func([188]byte)) {
	pn := uint16(1)
	pm := uint16(4095)
	pid := uint16(257)
	//esd := []byte{14,3,192,3,32}
	//	NAK & \r \255 \255 I D 3 ' ' 255 ID 3 ' ' \0 SI
	dsc := []byte{0x25, 255, 255, 73, 68, 51, 32, 255, 73, 68, 51, 32, 0, 3, 0, 1}
	esd := []byte{0x15, 38, 13, 255, 255, 73, 68, 51, 32, 255, 73, 68, 51, 32, 0, 15}
	return patpmt(pn, pm, pid, dsc, esd)
}

func patpmt(pn uint16, pm uint16, pid uint16, dsc []byte, esd []byte) func(func([188]byte)) {
	cc := byte(0)
	pmtd := make(map[uint16][]byte)
	pmtd[pid] = []byte{0x0f}
	pmtd[258] = esd
	return func(fn func([188]byte)) {
		fn(ProgramAssociationTable(pn, pm, cc))
		fn(ProgramMappingTable(pn, pm, cc, pid, dsc, pmtd))
		cc++
	}
}

func ProgramSpecificInformation(pn uint16, pmp uint16, pid uint16, dsc []byte, esd []byte, esdpid uint16) func() [][188]byte {
	cc := byte(0)
	pmtd := make(map[uint16][]byte)
	pmtd[pid] = []byte{0x0f}
	pmtd[esdpid] = esd
	return func() [][188]byte {
		pat := ProgramAssociationTable(pn, pmp, cc)
		pmt := ProgramMappingTable(pn, pmp, cc, pid, dsc, pmtd)
		cc++
		return [][188]byte{pat, pmt}
	}
}

// PAT
// PID=0
// SYNC|FLAGS|AF|PAYLOAD(PES|PSI)
// PSI(PAT|PMT)

// PSI = PF(1)|PFB(n)|/pf

func TableHeader(table_id uint8, ss []byte) []byte {
	ssl := uint16(len(ss))
	th := make([]byte, 3+ssl)

	th[0] = byte(table_id)
	//th[1] = 0xb0 | byte(ssl>>8 & 0x3)
	th[1] = (0x1 << 7) | (0x0 << 6) | (0x3 << 4) | (0x0 << 2) | byte(ssl>>8&0x3)
	th[2] = byte(ssl & 0xff)
	copy(th[3:], ss[:])

	o := len(th) - 4
	crc := CRC32(th[0:o])
	th[o+0] = byte(crc >> 24 & 0xff)
	th[o+1] = byte(crc >> 16 & 0xff)
	th[o+2] = byte(crc >> 8 & 0xff)
	th[o+3] = byte(crc & 0xff)

	return th
}

func TableSyntaxSection(tide uint16, td []byte) []byte {
	l := uint16(len(td))
	s := make([]byte, 5+l+4)

	s[0] = byte(tide >> 8 & 0xff)
	s[1] = byte(tide & 0xff)
	// 0b11000010 = 0xc2
	s[2] = 0xc0 | 0x0 | 0x1 // rb/vn/cni
	s[3] = 0x0              //sn
	s[4] = 0x0              //lsn

	copy(s[5:], td[:])
	return s
}

func AdaptationField(af []byte, _di bool, _rai bool, _espi bool, opt ...[]byte) []byte {
	// data is 183 bytes
	if len(af) == 1 {
		af[0] = 0
		return af
	}

	aflen := len(af)

	if af == nil || aflen == 0 {
		af = make([]byte, 188)
	}

	di := byte(0)    // Discontinuity indicator
	rai := byte(0)   // Random access indicator
	espi := byte(0)  // Elementary stream priority indicator
	pcrf := byte(0)  // PCR flag
	opcrf := byte(0) // OPCR flag
	spf := byte(0)   // Splicing point flag
	tpdf := byte(0)  // Transport private data flag
	afef := byte(0)  // Adaptation field extension flag

	if _di {
		di = 1
	}
	if _rai {
		rai = 1
	}
	if _espi {
		espi = 1
	}

	off := 2
	// pcr opcr spf tpd afe

	if len(opt) > 0 && opt[0] != nil {
		pcrf = 1
		pcr := af[off : off+6]
		if len(opt[0]) < 6 {
			copy(pcr[0:], opt[0][:])
		} else {
			copy(pcr[0:6], opt[0][:])
		}
		off += 6
	}

	if len(opt) > 1 && opt[1] != nil {
		opcrf = 1
		opcr := af[off : off+6]
		if len(opt[1]) < 6 {
			copy(opcr[0:], opt[1][:])
		} else {
			copy(opcr[0:6], opt[1][:])
		}
		off += 6
	}

	if len(opt) > 2 && opt[2] != nil {
		spf = 1
		if len(opt[1]) < 1 {
			af[off] = 0
		} else {
			af[off] = opt[2][0]
		}
		off += 1
	}

	if len(opt) > 3 && opt[3] != nil {
		tpdf = 1
		tpdl := len(opt[3])
		af[off] = byte(tpdl)
		off++
		tpd := af[off : off+tpdl]
		copy(tpd[0:], opt[3][:])
		off += tpdl
	}

	if len(opt) > 4 && opt[4] != nil {
		afef = 1
		afel := len(opt[4])
		af[off] = byte(afel)
		off++
		ae := af[off : off+afel]
		copy(ae[0:], opt[4][:])
		off += afel
	}

	if aflen == 0 {
		aflen = off
	} else {
		if aflen < off {
			return nil
		}
		for n := off; n < aflen; n++ {
			af[n] = 0xff
		}
	}

	af[0] = byte(aflen - 1)
	af[1] = di<<7 | rai<<6 | espi<<5 | pcrf<<4 | opcrf<<3 | spf<<2 | tpdf<<1 | afef

	return af[0:aflen]
}

// Program clock reference, stored as 33 bits base, 6 bits reserved, 9 bits extension.
// The value is calculated as base * 300 + extension.

func AFPCR(pcr uint64) []byte {
	p := make([]byte, 6)

	p[0] = byte(pcr >> 25 & 0xff)
	p[1] = byte(pcr >> 17 & 0xff)
	p[2] = byte(pcr >> 9 & 0xff)
	p[3] = byte(pcr >> 1 & 0xff)
	p[4] = byte(pcr << 7 & 0x80)
	p[5] = 0

	return p
}

func TransportStreamPacket(pusi bool, tp bool, pid uint16, cc uint8,
	af []byte, pd []byte) [188]byte {

	var tsp [188]byte

	if pusi {
		tsp[1] |= 0x40
	}
	if tp {
		tsp[1] |= 0x20
	}

	tsp[0] = 0x47
	tsp[1] |= byte(pid >> 8 & 0x1f)
	tsp[2] = byte(pid & 0xff)

	if af == nil && len(pd) > 0 && len(pd) != 184 {
		af = make([]byte, 184-len(pd))
		af = AdaptationField(af, false, false, false)
	}

	if len(af)+len(pd) != 184 {
		//log.Println(len(af)+len(pd), len(af), len(pd))
		tsp[1] |= 0x80
		return tsp
	}

	if len(af) > 0 {
		tsp[3] |= 0x20
	}

	if len(pd) > 0 {
		tsp[3] |= 0x10
	}

	tsp[3] |= byte(cc & 0xf)

	copy(tsp[4:], af[:])
	copy(tsp[4+len(af):], pd[:])

	//x := tsp[0:10]
	//fmt.Println(x)

	return tsp
}

func OptionalPESHeader(oph []byte, _dai bool, pts uint64) []byte {
	if len(oph) < 8 {
		return nil
	}

	mb := byte(0x2 & 0x03)
	sc := byte(0x00 & 0x03)
	pri := byte(0 & 0x1)
	dai := byte(0x0 & 0x1)
	if _dai {
		dai = byte(0x1 & 0x1)
	}
	c := byte(0x0 & 0x1)
	ooc := byte(0x0 & 0x1)

	//oph := make([]byte, 3 + len(of))
	ptsdts := byte(0x2)

	oph[0] = (mb << 6) | (sc << 4) | (pri << 3) | (dai << 2) | (c << 1) | ooc
	oph[1] = (ptsdts << 6)
	oph[2] = byte(len(oph) - 3)

	//oph[3] = (byte(pts>>29) & 0x0f) | 0x31
	oph[3] = (byte(pts>>29) & 0x0e) | 0x21
	oph[4] = (byte(pts>>22) & 0xff)
	oph[5] = (byte(pts>>14) & 0xfe) | 0x1
	oph[6] = (byte(pts>>7) & 0xff)
	oph[7] = (byte(pts<<1) & 0xfe) | 0x1

	for n := 8; n < len(oph); n++ {
		oph[n] = 0xff
	}

	// While above flags indicate that values are appended into
	// variable length optional fields, they are not just simply
	// written out. For example, PTS (and DTS) is expanded from 33
	// bits to 5 bytes (40 bits). If only PTS is present, this is
	// done by catenating 0010b, most significant 3 bits from PTS, 1,
	// following next 15 bits, 1, rest 15 bits and 1. If both PTS and
	// DTS are present, first 4 bits are 0011 and first 4 bits for
	// DTS are 0001. Other appended bytes have similar but different
	// encoding.

	//0010bbb1 bbbbbbbb bbbbbbb1 bbbbbbbb bbbbbbb1
	//0011
	//0001

	return oph
}

func PESPacket(sid uint8, oph []byte, data []byte) []byte {
	if len(oph) < 3 {
		return nil
	}
	ppl := len(oph) + len(data)
	pes := make([]byte, 6+len(oph)+len(data))
	pes[0] = 0x00
	pes[1] = 0x00
	pes[2] = 0x01
	pes[3] = byte(sid)
	pes[4] = byte((ppl >> 8) & 0xff)
	pes[5] = byte(ppl & 0xff)
	copy(pes[6:], oph[:])
	copy(pes[(6+len(oph)):], data[:])
	return pes
}

var crc_table []uint32 = nil

func CRC32(data []byte) uint32 {
	crc := uint32(0xffffffff)

	if crc_table == nil {
		var crc uint32
		crc32_poly := uint32(0x04c11db7)
		crc_table = make([]uint32, 256)
		for i := uint32(0); i < 256; i++ {
			crc = i << 24
			for j := uint32(0); j < 8; j++ {
				if crc&0x80000000 != 0 {
					crc = (crc << 1) ^ crc32_poly
				} else {
					crc = (crc << 1)
				}
			}
			crc_table[i] = crc
		}
	}

	for j := 0; j < len(data); j++ {
		i := ((crc >> 24) ^ uint32(data[j])) & 0xff
		crc = (crc << 8) ^ crc_table[i]
	}
	return crc
}

type TS [188]byte

func (t TS) TEI() bool {
	return t[1]&0x80 != 0
}

func (t TS) PUSI() bool {
	return t[1]&0x40 != 0
}

func (t TS) PID() uint16 {
	return uint16(t[1]&0x1f)<<8 | uint16(t[2])
}
