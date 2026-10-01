package bilibili

import (
	"encoding/base64"
	"testing"
)

// 手动构建一个 Dm protobuf 消息
// Dm 结构：field 1(id_str)=string, field 4(color)=varint, field 6(content)=string
func buildTestDm(content string, color uint32) []byte {
	var buf []byte

	// field 1: id_str (wire type 2, field 1 → tag = (1<<3)|2 = 0x0A)
	buf = append(buf, 0x0A)
	buf = appendVarint(buf, uint64(len("test_id")))
	buf = append(buf, []byte("test_id")...)

	// field 4: color (wire type 0, field 4 → tag = (4<<3)|0 = 0x20)
	if color > 0 {
		buf = append(buf, 0x20)
		buf = appendVarint(buf, uint64(color))
	}

	// field 6: content (wire type 2, field 6 → tag = (6<<3)|2 = 0x32)
	buf = append(buf, 0x32)
	buf = appendVarint(buf, uint64(len(content)))
	buf = append(buf, []byte(content)...)

	return buf
}

func appendVarint(buf []byte, val uint64) []byte {
	for val >= 0x80 {
		buf = append(buf, byte(val)|0x80)
		val >>= 7
	}
	buf = append(buf, byte(val))
	return buf
}

func TestExtractProtobufString(t *testing.T) {
	data := buildTestDm("hello world", 16777215)

	content, ok := extractProtobufString(data, 6)
	if !ok {
		t.Fatal("failed to extract content field")
	}
	if content != "hello world" {
		t.Fatalf("expected 'hello world', got '%s'", content)
	}

	idStr, ok := extractProtobufString(data, 1)
	if !ok {
		t.Fatal("failed to extract id_str field")
	}
	if idStr != "test_id" {
		t.Fatalf("expected 'test_id', got '%s'", idStr)
	}
}

func TestExtractProtobufUint32(t *testing.T) {
	data := buildTestDm("test", 16777215)

	color, ok := extractProtobufUint32(data, 4)
	if !ok {
		t.Fatal("failed to extract color field")
	}
	if color != 16777215 {
		t.Fatalf("expected 16777215, got %d", color)
	}

	// 不存在的字段
	_, ok = extractProtobufUint32(data, 99)
	if ok {
		t.Fatal("should not find non-existent field")
	}
}

func TestExtractProtobufStringNotFound(t *testing.T) {
	data := buildTestDm("test", 0)

	// 不存在的字段号
	_, ok := extractProtobufString(data, 99)
	if ok {
		t.Fatal("should not find non-existent field")
	}

	// 空数据
	_, ok = extractProtobufString([]byte{}, 6)
	if ok {
		t.Fatal("should not find field in empty data")
	}
}

func TestDmV2Base64Decode(t *testing.T) {
	data := buildTestDm("仅在dm_v2中的弹幕", 255)
	encoded := base64.StdEncoding.EncodeToString(data)

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}

	content, ok := extractProtobufString(decoded, 6)
	if !ok {
		t.Fatal("failed to extract content from decoded dm_v2")
	}
	if content != "仅在dm_v2中的弹幕" {
		t.Fatalf("expected '仅在dm_v2中的弹幕', got '%s'", content)
	}

	color, ok := extractProtobufUint32(decoded, 4)
	if !ok {
		t.Fatal("failed to extract color from decoded dm_v2")
	}
	if color != 255 {
		t.Fatalf("expected 255, got %d", color)
	}
}

func TestDmV2WithEmptyContent(t *testing.T) {
	// dm_v2 中 content 为空的情况
	data := buildTestDm("", 0)
	encoded := base64.StdEncoding.EncodeToString(data)

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}

	content, ok := extractProtobufString(decoded, 6)
	if !ok {
		t.Fatal("should find empty content field")
	}
	if content != "" {
		t.Fatalf("expected empty string, got '%s'", content)
	}
}
