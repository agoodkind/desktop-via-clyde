package computeruseext

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
)

const (
	machOMagic32          = 0xfeedface
	machOMagic64          = 0xfeedfacf
	fatMachOMagic32       = 0xcafebabe
	fatMachOMagic64       = 0xcafebabf
	machOCodeSignatureCmd = 0x1d
)

type computerUseByteRange struct {
	start int
	end   int
}

func countTrustedTeamTokens(data []byte, teamID string) (int, error) {
	signatureRanges, err := readMachOCodeSignatureRanges(data)
	if err != nil {
		return 0, err
	}
	teamBytes := []byte(teamID)
	count := 0
	for offset := 0; ; {
		index := bytes.Index(data[offset:], teamBytes)
		if index < 0 {
			return count, nil
		}
		start := offset + index
		end := start + len(teamBytes)
		if hasStandaloneTokenBoundary(data, start, end) && !rangeOverlapsAny(start, end, signatureRanges) {
			count++
		}
		offset = end
	}
}

func readMachOCodeSignatureRanges(data []byte) ([]computerUseByteRange, error) {
	if len(data) < 4 {
		return nil, nil
	}
	magicBytes := data[:4]
	switch {
	case binary.BigEndian.Uint32(magicBytes) == fatMachOMagic32:
		return readFatMachOCodeSignatureRanges(data, binary.BigEndian, false)
	case binary.LittleEndian.Uint32(magicBytes) == fatMachOMagic32:
		return readFatMachOCodeSignatureRanges(data, binary.LittleEndian, false)
	case binary.BigEndian.Uint32(magicBytes) == fatMachOMagic64:
		return readFatMachOCodeSignatureRanges(data, binary.BigEndian, true)
	case binary.LittleEndian.Uint32(magicBytes) == fatMachOMagic64:
		return readFatMachOCodeSignatureRanges(data, binary.LittleEndian, true)
	default:
		return readThinMachOCodeSignatureRanges(data, 0)
	}
}

func readFatMachOCodeSignatureRanges(data []byte, order binary.ByteOrder, is64 bool) ([]computerUseByteRange, error) {
	if len(data) < 8 {
		return nil, fmt.Errorf("truncated fat Mach-O header")
	}
	architectureCount := uint64(order.Uint32(data[4:8]))
	entrySize := uint64(20)
	if is64 {
		entrySize = 32
	}
	headerEnd := uint64(8) + architectureCount*entrySize
	if headerEnd > uint64(len(data)) {
		return nil, fmt.Errorf("truncated fat Mach-O architecture table")
	}
	ranges := make([]computerUseByteRange, 0, architectureCount)
	for architectureIndex := range architectureCount {
		entryOffset := uint64(8) + architectureIndex*entrySize
		var sliceOffset uint64
		var sliceSize uint64
		if is64 {
			sliceOffset = order.Uint64(data[entryOffset+8 : entryOffset+16])
			sliceSize = order.Uint64(data[entryOffset+16 : entryOffset+24])
		} else {
			sliceOffset = uint64(order.Uint32(data[entryOffset+8 : entryOffset+12]))
			sliceSize = uint64(order.Uint32(data[entryOffset+12 : entryOffset+16]))
		}
		sliceEnd := sliceOffset + sliceSize
		if sliceEnd < sliceOffset || sliceEnd > uint64(len(data)) {
			return nil, fmt.Errorf("fat Mach-O architecture %d exceeds file bounds", architectureIndex)
		}
		sliceBaseOffset, err := computerUseByteIndex(sliceOffset)
		if err != nil {
			return nil, err
		}
		sliceRanges, err := readThinMachOCodeSignatureRanges(data[sliceOffset:sliceEnd], sliceBaseOffset)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, sliceRanges...)
	}
	return ranges, nil
}

func readThinMachOCodeSignatureRanges(data []byte, baseOffset int) ([]computerUseByteRange, error) {
	if len(data) < 4 {
		return nil, nil
	}
	order, is64, ok := thinMachOFormat(data[:4])
	if !ok {
		return nil, nil
	}
	headerSize := 28
	if is64 {
		headerSize = 32
	}
	if len(data) < headerSize {
		return nil, fmt.Errorf("truncated Mach-O header")
	}
	commandCount := order.Uint32(data[16:20])
	commandsSize := uint64(order.Uint32(data[20:24]))
	commandsEnd := uint64(headerSize) + commandsSize
	if commandsEnd > uint64(len(data)) {
		return nil, fmt.Errorf("Mach-O load commands exceed file bounds")
	}
	commandOffset := uint64(headerSize)
	ranges := make([]computerUseByteRange, 0, 1)
	for commandIndex := range commandCount {
		if commandOffset+8 > commandsEnd {
			return nil, fmt.Errorf("Mach-O load command %d is truncated", commandIndex)
		}
		command := order.Uint32(data[commandOffset : commandOffset+4])
		commandSize := uint64(order.Uint32(data[commandOffset+4 : commandOffset+8]))
		if commandSize < 8 || commandOffset+commandSize > commandsEnd {
			return nil, fmt.Errorf("Mach-O load command %d has invalid size", commandIndex)
		}
		if command == machOCodeSignatureCmd {
			if commandSize < 16 {
				return nil, fmt.Errorf("Mach-O code signature command is truncated")
			}
			signatureOffset := uint64(order.Uint32(data[commandOffset+8 : commandOffset+12]))
			signatureSize := uint64(order.Uint32(data[commandOffset+12 : commandOffset+16]))
			signatureEnd := signatureOffset + signatureSize
			if signatureEnd < signatureOffset || signatureEnd > uint64(len(data)) {
				return nil, fmt.Errorf("Mach-O code signature exceeds file bounds")
			}
			signatureStartIndex, err := computerUseByteIndex(signatureOffset)
			if err != nil {
				return nil, err
			}
			signatureEndIndex, err := computerUseByteIndex(signatureEnd)
			if err != nil {
				return nil, err
			}
			ranges = append(ranges, computerUseByteRange{
				start: baseOffset + signatureStartIndex,
				end:   baseOffset + signatureEndIndex,
			})
		}
		commandOffset += commandSize
	}
	return ranges, nil
}

func computerUseByteIndex(value uint64) (int, error) {
	index, err := strconv.Atoi(strconv.FormatUint(value, 10))
	if err != nil {
		return 0, fmt.Errorf("byte offset %d exceeds platform int", value)
	}
	return index, nil
}

func thinMachOFormat(magic []byte) (binary.ByteOrder, bool, bool) {
	if binary.LittleEndian.Uint32(magic) == machOMagic32 {
		return binary.LittleEndian, false, true
	}
	if binary.LittleEndian.Uint32(magic) == machOMagic64 {
		return binary.LittleEndian, true, true
	}
	if binary.BigEndian.Uint32(magic) == machOMagic32 {
		return binary.BigEndian, false, true
	}
	if binary.BigEndian.Uint32(magic) == machOMagic64 {
		return binary.BigEndian, true, true
	}
	return nil, false, false
}

func rangeOverlapsAny(start int, end int, ranges []computerUseByteRange) bool {
	for _, candidate := range ranges {
		if start < candidate.end && end > candidate.start {
			return true
		}
	}
	return false
}
