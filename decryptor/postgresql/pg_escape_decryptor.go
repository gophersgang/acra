// Copyright 2016, Cossack Labs Limited
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package postgresql

import (
	"bytes"
	"encoding/binary"
	"github.com/cossacklabs/acra/utils"
	"io"
	"log"
	"strconv"

	"fmt"
	"github.com/cossacklabs/acra/decryptor/base"
	"github.com/cossacklabs/acra/keystore"
	"github.com/cossacklabs/acra/zone"
	"github.com/cossacklabs/themis/gothemis/cell"
	"github.com/cossacklabs/themis/gothemis/keys"
	"github.com/cossacklabs/themis/gothemis/message"
)

var ESCAPE_TAG_BEGIN = EncodeToOctal(base.TAG_BEGIN)

var ESCAPE_ZONE_TAG_LENGTH = zone.ZONE_TAG_LENGTH
var ESCAPE_ZONE_ID_BLOCK_LENGTH = zone.ZONE_ID_BLOCK_LENGTH

func encodeToOctal(from, to []byte) {
	to = to[:0]
	for _, c := range from {
		if utils.IsPrintableEscapeChar(c) {
			if c == utils.SLASH_CHAR {
				to = append(to, []byte{utils.SLASH_CHAR, utils.SLASH_CHAR}...)
			} else {
				to = append(to, c)
			}
		} else {
			to = append(to, utils.SLASH_CHAR)
			octal := strconv.FormatInt(int64(c), 8)
			switch len(octal) {
			case 3:
				to = append(to, []byte(octal)...)
			case 2:
				to = append(to, '0', octal[0], octal[1])

			case 1:
				to = append(to, '0', '0', octal[0])
			}
		}
	}
}

func EncodeToOctal(from []byte) []byte {
	// count output size
	outputLength := 0
	for _, c := range from {
		if utils.IsPrintableEscapeChar(c) {
			if c == utils.SLASH_CHAR {
				outputLength += 2
			} else {
				outputLength++
			}
		} else {
			outputLength += 4
		}
	}
	buffer := make([]byte, outputLength)
	encodeToOctal(from, buffer)
	return buffer
}

type PgEscapeDecryptor struct {
	currentIndex    uint8
	outputSize      int
	isWithZone      bool
	poisonKey       []byte
	callbackStorage *base.PoisonCallbackStorage
	// max size can be 4 characters for octal representation per byte
	octKeyBlockBuffer     [base.KEY_BLOCK_LENGTH * 4]byte
	decodedKeyBlockBuffer []byte
	//uint64
	lengthBuf [8]byte
	// 4 oct symbols (\000) ber byte
	octLengthBuf [8 * 4]byte
	octCharBuf   [3]byte
	keyStore     keystore.KeyStore
	zoneMatcher  *zone.ZoneIdMatcher
}

func NewPgEscapeDecryptor() *PgEscapeDecryptor {
	return &PgEscapeDecryptor{
		currentIndex:          0,
		isWithZone:            false,
		outputSize:            0,
		decodedKeyBlockBuffer: make([]byte, base.KEY_BLOCK_LENGTH),
	}
}

func (decryptor *PgEscapeDecryptor) MatchBeginTag(char byte) bool {
	if char == ESCAPE_TAG_BEGIN[decryptor.currentIndex] {
		decryptor.currentIndex++
		decryptor.outputSize++
		return true
	}
	return false
}
func (decryptor *PgEscapeDecryptor) IsMatched() bool {
	return int(decryptor.currentIndex) == len(ESCAPE_TAG_BEGIN)
}
func (decryptor *PgEscapeDecryptor) Reset() {
	decryptor.currentIndex = 0
	decryptor.outputSize = 0
}
func (decryptor *PgEscapeDecryptor) GetMatched() []byte {
	return ESCAPE_TAG_BEGIN[:decryptor.currentIndex]
}

func (decryptor *PgEscapeDecryptor) readOctalData(data, octData []byte, reader io.Reader) (int, int, error) {
	dataIndex := 0
	octDataIndex := 0
	var charBuf [1]byte
	for {
		n, err := reader.Read(charBuf[:])
		if err != nil {
			return dataIndex, octDataIndex, err
		}
		if n != 1 {
			log.Println("Debug: readOctalData read 0 bytes")
			return dataIndex, octDataIndex, base.ErrFakeAcraStruct
		}
		octData[octDataIndex] = charBuf[0]
		octDataIndex++
		if !utils.IsPrintableEscapeChar(charBuf[0]) {
			return dataIndex, octDataIndex, base.ErrFakeAcraStruct
		}

		// if slash than next char must be slash too
		if charBuf[0] == utils.SLASH_CHAR {
			// read next char
			_, err := reader.Read(charBuf[:])
			if err != nil {
				return dataIndex, octDataIndex, err
			}
			octData[octDataIndex] = charBuf[0]
			octDataIndex++
			if charBuf[0] == utils.SLASH_CHAR {
				// just write slash char
				data[dataIndex] = charBuf[0]
				dataIndex++
			} else {
				decryptor.octCharBuf[0] = charBuf[0]
				// read next 3 oct bytes
				n, err := io.ReadFull(reader, decryptor.octCharBuf[1:])
				if err != nil {
					return dataIndex, octDataIndex, err
				}
				if n != len(decryptor.octCharBuf)-1 {
					if n != 0 {
						copy(octData[octDataIndex:octDataIndex+n], decryptor.octCharBuf[1:1+n])
						octDataIndex += n
					}
					log.Printf("Warning: expected 2 octal symbols, but read %v\n", n)
					return dataIndex, octDataIndex, base.ErrFakeAcraStruct
				}
				// parse 3 octal symbols
				num, err := strconv.ParseInt(string(decryptor.octCharBuf[:]), 8, 9)
				if err != nil {
					return dataIndex, octDataIndex, base.ErrFakeAcraStruct
				}
				data[dataIndex] = byte(num)
				dataIndex++

				copy(octData[octDataIndex:octDataIndex+len(decryptor.octCharBuf)-1], decryptor.octCharBuf[1:])
				octDataIndex += 2
			}
		} else {
			// just write to data
			data[dataIndex] = charBuf[0]
			dataIndex++
		}
		if dataIndex == cap(data) {
			return dataIndex, octDataIndex, nil
		}
	}
}

func (decryptor *PgEscapeDecryptor) ReadSymmetricKey(privateKey *keys.PrivateKey, reader io.Reader) ([]byte, []byte, error) {
	dataLength, octDataLength, err := decryptor.readOctalData(decryptor.decodedKeyBlockBuffer, decryptor.octKeyBlockBuffer[:], reader)
	if err != nil {
		return nil, decryptor.octKeyBlockBuffer[:octDataLength], err
	}
	if len(decryptor.decodedKeyBlockBuffer) != base.KEY_BLOCK_LENGTH || dataLength != base.KEY_BLOCK_LENGTH {
		return nil, decryptor.octKeyBlockBuffer[:octDataLength], base.ErrFakeAcraStruct
	}
	smessage := message.New(privateKey, &keys.PublicKey{Value: decryptor.decodedKeyBlockBuffer[:base.PUBLIC_KEY_LENGTH]})
	symmetricKey, err := smessage.Unwrap(decryptor.decodedKeyBlockBuffer[base.PUBLIC_KEY_LENGTH:])
	if err != nil {
		return nil, decryptor.octKeyBlockBuffer[:octDataLength], base.ErrFakeAcraStruct
	}
	decryptor.outputSize += octDataLength
	return symmetricKey, decryptor.octKeyBlockBuffer[:octDataLength], nil
}

func (decryptor *PgEscapeDecryptor) readDataLength(reader io.Reader) (uint64, []byte, error) {
	var length uint64

	lenCount, octLenCount, err := decryptor.readOctalData(decryptor.lengthBuf[:], decryptor.octLengthBuf[:], reader)
	if err != nil {
		log.Printf("Warning: %v\n", utils.ErrorMessage("can't read data length", err))
		return 0, decryptor.octLengthBuf[:octLenCount], err
	}
	if lenCount != len(decryptor.lengthBuf) {
		log.Printf("Warning: incorrect length count, %v!=%v\n", lenCount, len(decryptor.lengthBuf))
		return 0, decryptor.octLengthBuf[:octLenCount], base.ErrFakeAcraStruct
	}
	decryptor.outputSize += octLenCount
	binary.Read(bytes.NewBuffer(decryptor.lengthBuf[:]), binary.LittleEndian, &length)
	return length, decryptor.octLengthBuf[:octLenCount], nil
}
func (decryptor *PgEscapeDecryptor) readScellData(length uint64, reader io.Reader) ([]byte, []byte, error) {
	hexBuf := make([]byte, int(length)*4)
	buf := make([]byte, int(length))
	n, octN, err := decryptor.readOctalData(buf, hexBuf, reader)
	if err != nil {
		log.Printf("Warning: %v\n", utils.ErrorMessage(fmt.Sprintf("can't read scell data with passed length=%v", length), err))
		return nil, hexBuf[:octN], err
	}
	if n != int(length) {
		log.Printf("Warning: read incorrect length, %v!=%v\n", n, length)
		return nil, hexBuf[:octN], base.ErrFakeAcraStruct
	}
	decryptor.outputSize += octN
	return buf, hexBuf[:octN], nil
}

func (decryptor *PgEscapeDecryptor) getFullDataLength() int {
	return decryptor.outputSize
}

func (decryptor *PgEscapeDecryptor) ReadData(symmetricKey, zoneId []byte, reader io.Reader) ([]byte, error) {
	length, hexLengthBuf, err := decryptor.readDataLength(reader)
	if err != nil {
		return hexLengthBuf, err
	}
	data, octData, err := decryptor.readScellData(length, reader)
	if err != nil {
		return append(hexLengthBuf, octData...), err
	}

	scell := cell.New(symmetricKey, cell.CELL_MODE_SEAL)
	decrypted, err := scell.Unprotect(data, nil, zoneId)
	// fill zero symmetric_key
	utils.FillSlice(byte(0), symmetricKey[:])
	if err != nil {
		return append(hexLengthBuf, octData...), base.ErrFakeAcraStruct
	}
	return EncodeToOctal(decrypted), nil
}

func (decryptor *PgEscapeDecryptor) GetTagBeginLength() int {
	return len(ESCAPE_TAG_BEGIN)
}
