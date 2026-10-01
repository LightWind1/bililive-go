package bilibili

import "encoding/binary"

// extractProtobufString 从 protobuf 二进制数据中提取指定字段号的字符串值
// 仅支持 wire type 2（length-delimited: bytes/string）
func extractProtobufString(data []byte, targetField uint32) (string, bool) {
	cursor := 0
	for cursor < len(data) {
		// 读取 tag（varint 编码的 field_number << 3 | wire_type）
		tag, n := binary.Uvarint(data[cursor:])
		if n <= 0 {
			break
		}
		cursor += n

		fieldNumber := uint32(tag >> 3)
		wireType := tag & 0x7

		switch wireType {
		case 0: // varint
			_, n := binary.Uvarint(data[cursor:])
			if n <= 0 {
				return "", false
			}
			cursor += n
		case 1: // 64-bit
			if cursor+8 > len(data) {
				return "", false
			}
			cursor += 8
		case 2: // length-delimited
			length, n := binary.Uvarint(data[cursor:])
			if n <= 0 {
				return "", false
			}
			cursor += n
			if length > uint64(len(data)-cursor) {
				return "", false
			}
			end := cursor + int(length)
			if fieldNumber == targetField {
				return string(data[cursor:end]), true
			}
			cursor = end
		case 5: // 32-bit
			if cursor+4 > len(data) {
				return "", false
			}
			cursor += 4
		default:
			return "", false
		}
	}
	return "", false
}

// extractProtobufUint32 从 protobuf 二进制数据中提取指定字段号的 uint32 值
// 仅支持 wire type 0（varint）
func extractProtobufUint32(data []byte, targetField uint32) (uint32, bool) {
	cursor := 0
	for cursor < len(data) {
		tag, n := binary.Uvarint(data[cursor:])
		if n <= 0 {
			break
		}
		cursor += n

		fieldNumber := uint32(tag >> 3)
		wireType := tag & 0x7

		switch wireType {
		case 0: // varint
			val, n := binary.Uvarint(data[cursor:])
			if n <= 0 {
				return 0, false
			}
			cursor += n
			if fieldNumber == targetField {
				return uint32(val), true
			}
		case 1: // 64-bit
			if cursor+8 > len(data) {
				return 0, false
			}
			cursor += 8
		case 2: // length-delimited
			length, n := binary.Uvarint(data[cursor:])
			if n <= 0 {
				return 0, false
			}
			cursor += n
			if length > uint64(len(data)-cursor) {
				return 0, false
			}
			cursor += int(length)
		case 5: // 32-bit
			if cursor+4 > len(data) {
				return 0, false
			}
			cursor += 4
		default:
			return 0, false
		}
	}
	return 0, false
}
