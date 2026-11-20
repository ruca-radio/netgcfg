package rax

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// magicPrefix identifies a Netgear Broadcom (RAX-series) config backup. The
// file starts with a 40-byte header: a 2-byte version (0x0002), a 2-byte
// length (0x0020 = 32), a 32-byte signature (see signConfig), and 4 reserved
// zero bytes.
var magicPrefix = []byte{0x00, 0x02, 0x00, 0x20}

// signSaltPrefix/signSaltSuffix are the hardcoded constants prepended/appended
// to the signing payload by the device firmware (puUtl_signCfg in libpu_util).
const (
	signSaltPrefix = "hr89sdfgjkehx"
	signSaltSuffix = "nohsli9fjh3f"
)

const (
	headerSize      = 40
	descriptionSize = 60 // "<compressed ...>" field + "<crc ...>" field
	field1Size      = 40 // "<compressed alg=lzw len=NNN>", NUL-padded
	field2Size      = 20 // "<crc=0xXXXXXXXX>", NUL-padded

	// TLV record types found in the container.
	tlvPayload     = 1
	tlvFirmware    = 2
	tlvFirmwareMaj = 3
	tlvModel       = 4
)

// Config is a parsed Netgear RAX/Broadcom configuration backup.
type Config struct {
	header        [headerSize]byte
	firmware      string // TLV type 2, e.g. "V1.0.0.0"
	firmwareMajor string // TLV type 3, e.g. "V1.0"
	model         string // TLV type 4, e.g. "RAX30"
	configXML     []byte // decompressed config: the XML tree plus a trailing NUL
}

// IsRAXConfig reports whether b looks like a Netgear RAX/Broadcom config backup.
func IsRAXConfig(b []byte) bool {
	return len(b) >= headerSize && bytes.HasPrefix(b, magicPrefix)
}

// Decrypt parses a config backup and decompresses its XML payload.
func Decrypt(data []byte) (*Config, error) {
	if !IsRAXConfig(data) {
		return nil, errors.New("not a Netgear RAX/Broadcom config backup")
	}
	c := &Config{}
	copy(c.header[:], data[:headerSize])

	pos := headerSize
	for {
		if pos+4 > len(data) {
			return nil, errors.New("compressed config section not found")
		}
		typ := binary.BigEndian.Uint16(data[pos : pos+2])
		length := binary.BigEndian.Uint16(data[pos+2 : pos+4])

		if typ == tlvPayload {
			if pos+4+descriptionSize > len(data) {
				return nil, errors.New("truncated compressed config section")
			}
			comp := data[pos+4+descriptionSize:]
			xml, err := lzwDecompress(comp)
			if err != nil {
				return nil, fmt.Errorf("lzw decompress: %w", err)
			}
			c.configXML = xml
			return c, nil
		}

		if pos+4+int(length) > len(data) {
			return nil, errors.New("truncated TLV record")
		}
		val := data[pos+4 : pos+4+int(length)]
		switch typ {
		case tlvFirmware:
			c.firmware = string(val)
		case tlvFirmwareMaj:
			c.firmwareMajor = string(val)
		case tlvModel:
			c.model = string(val)
		}
		pos += 4 + int(length)
	}
}

// Encrypt compresses the XML payload and rebuilds a config backup, recomputing
// the payload length, JAMCRC checksum, and header signature.
func (c *Config) Encrypt() ([]byte, error) {
	comp := lzwCompress(c.configXML)
	crc := jamCRC(comp)

	desc := append(
		padNUL([]byte(fmt.Sprintf("<compressed alg=lzw len=%d>", len(comp))), field1Size),
		padNUL([]byte(fmt.Sprintf("<crc=0x%x>", crc)), field2Size)...)

	var buf bytes.Buffer
	writeTLV(&buf, tlvFirmware, []byte(c.firmware))
	writeTLV(&buf, tlvFirmwareMaj, []byte(c.firmwareMajor))
	writeTLV(&buf, tlvModel, []byte(c.model))
	writeTLV(&buf, tlvPayload, nil)

	// Recompute the header signature over: 4 zero bytes + TLV records + the
	// description block + the compressed payload, bracketed by two salts.
	sig := signConfig(buf.Bytes(), desc, comp)

	header := c.header
	copy(header[4:36], sig)

	out := make([]byte, 0, headerSize+len(buf.Bytes())+len(desc)+len(comp))
	out = append(out, header[:]...)
	out = append(out, buf.Bytes()...)
	out = append(out, desc...)
	out = append(out, comp...)
	return out, nil
}

// signConfig computes the 32-byte header signature the device verifies on
// restore. The input is the TLV record block, the description block, and the
// compressed payload, wrapped in the firmware's two hardcoded salts.
func signConfig(tlvBlock, desc, comp []byte) []byte {
	var b bytes.Buffer
	b.WriteString(signSaltPrefix)
	b.Write([]byte{0, 0, 0, 0}) // four leading zero bytes before the TLV records
	b.Write(tlvBlock)
	b.Write(desc)
	b.Write(comp)
	b.WriteString(signSaltSuffix)
	sum := sha256.Sum256(b.Bytes())
	return sum[:]
}

func writeTLV(buf *bytes.Buffer, typ uint16, val []byte) {
	var t [2]byte
	binary.BigEndian.PutUint16(t[:], typ)
	buf.Write(t[:])
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(val)))
	buf.Write(l[:])
	buf.Write(val)
}

// padNUL returns b left-justified and NUL-padded to n bytes.
func padNUL(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	out := make([]byte, n)
	copy(out, b)
	return out
}

// jamCRC is the checksum used by these devices: the bitwise complement of the
// standard CRC-32 (IEEE 802.3) over the compressed payload.
func jamCRC(data []byte) uint32 {
	return ^crc32.ChecksumIEEE(data)
}
