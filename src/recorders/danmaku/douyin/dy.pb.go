// Code generated from dy.proto. DO NOT EDIT.

package douyin

import (
	"google.golang.org/protobuf/encoding/protowire"
)

// HeadersItem — HTTP 头部键值对
type HeadersItem struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (x *HeadersItem) Marshal() ([]byte, error) {
	var buf []byte
	if x.Key != "" {
		buf = protowire.AppendVarint(buf, (1<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendString(buf, x.Key)
	}
	if x.Value != "" {
		buf = protowire.AppendVarint(buf, (2<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendString(buf, x.Value)
	}
	return buf, nil
}

func (x *HeadersItem) Unmarshal(data []byte) error {
	*x = HeadersItem{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		switch typ {
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 1:
				x.Key = string(v)
			case 2:
				x.Value = string(v)
			}
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

// PushFrame — WebSocket 传输帧
type PushFrame struct {
	SeqId           uint64         `json:"seqId"`
	LogId           uint64         `json:"logId"`
	Service         uint64         `json:"service"`
	Method          uint64         `json:"method"`
	HeadersList     []*HeadersItem `json:"headersList"`
	PayloadEncoding string         `json:"payloadEncoding"`
	PayloadType     string         `json:"payloadType"`
	Payload         []byte         `json:"payload"`
}

func (x *PushFrame) Marshal() ([]byte, error) {
	var buf []byte
	if x.SeqId != 0 {
		buf = protowire.AppendVarint(buf, (1<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, x.SeqId)
	}
	if x.LogId != 0 {
		buf = protowire.AppendVarint(buf, (2<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, x.LogId)
	}
	if x.Service != 0 {
		buf = protowire.AppendVarint(buf, (3<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, x.Service)
	}
	if x.Method != 0 {
		buf = protowire.AppendVarint(buf, (4<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, x.Method)
	}
	for _, item := range x.HeadersList {
		b, err := item.Marshal()
		if err != nil {
			return nil, err
		}
		buf = protowire.AppendVarint(buf, (5<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendBytes(buf, b)
	}
	if x.PayloadEncoding != "" {
		buf = protowire.AppendVarint(buf, (6<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendString(buf, x.PayloadEncoding)
	}
	if x.PayloadType != "" {
		buf = protowire.AppendVarint(buf, (7<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendString(buf, x.PayloadType)
	}
	if len(x.Payload) > 0 {
		buf = protowire.AppendVarint(buf, (8<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendBytes(buf, x.Payload)
	}
	return buf, nil
}

func (x *PushFrame) Unmarshal(data []byte) error {
	*x = PushFrame{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 1:
				x.SeqId = v
			case 2:
				x.LogId = v
			case 3:
				x.Service = v
			case 4:
				x.Method = v
			}
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 5:
				item := &HeadersItem{}
				if err := item.Unmarshal(v); err != nil {
					return err
				}
				x.HeadersList = append(x.HeadersList, item)
			case 6:
				x.PayloadEncoding = string(v)
			case 7:
				x.PayloadType = string(v)
			case 8:
				x.Payload = v
			}
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

// Response — GZIP 解压后的容器
type Response struct {
	MessagesList      []*Message `json:"messagesList"`
	Cursor            string     `json:"cursor"`
	FetchInterval     uint64     `json:"fetchInterval"`
	Now               uint64     `json:"now"`
	InternalExt       string     `json:"internalExt"`
	FetchType         uint32     `json:"fetchType"`
	HeartbeatDuration uint64     `json:"heartbeatDuration"`
	NeedAck           bool       `json:"needAck"`
	HistoryNoMore     bool       `json:"historyNoMore"`
}

func (x *Response) Marshal() ([]byte, error) {
	var buf []byte
	for _, msg := range x.MessagesList {
		b, err := msg.Marshal()
		if err != nil {
			return nil, err
		}
		buf = protowire.AppendVarint(buf, (1<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendBytes(buf, b)
	}
	if x.Cursor != "" {
		buf = protowire.AppendVarint(buf, (2<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendString(buf, x.Cursor)
	}
	if x.FetchInterval != 0 {
		buf = protowire.AppendVarint(buf, (3<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, x.FetchInterval)
	}
	if x.Now != 0 {
		buf = protowire.AppendVarint(buf, (4<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, x.Now)
	}
	if x.InternalExt != "" {
		buf = protowire.AppendVarint(buf, (5<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendString(buf, x.InternalExt)
	}
	if x.FetchType != 0 {
		buf = protowire.AppendVarint(buf, (6<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, uint64(x.FetchType))
	}
	if x.HeartbeatDuration != 0 {
		buf = protowire.AppendVarint(buf, (8<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, x.HeartbeatDuration)
	}
	if x.NeedAck {
		buf = protowire.AppendVarint(buf, (9<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, 1)
	}
	if x.HistoryNoMore {
		buf = protowire.AppendVarint(buf, (12<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, 1)
	}
	return buf, nil
}

func (x *Response) Unmarshal(data []byte) error {
	*x = Response{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 3:
				x.FetchInterval = v
			case 4:
				x.Now = v
			case 6:
				x.FetchType = uint32(v)
			case 8:
				x.HeartbeatDuration = v
			case 9:
				x.NeedAck = v != 0
			case 12:
				x.HistoryNoMore = v != 0
			}
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 1:
				msg := &Message{}
				if err := msg.Unmarshal(v); err != nil {
					return err
				}
				x.MessagesList = append(x.MessagesList, msg)
			case 2:
				x.Cursor = string(v)
			case 5:
				x.InternalExt = string(v)
			}
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

// Message — 单条消息
type Message struct {
	Method  string `json:"method"`
	Payload []byte `json:"payload"`
	MsgId   int64  `json:"msgId"`
	MsgType int64  `json:"msgType"`
	Offset  int64  `json:"offset"`
}

func (x *Message) Marshal() ([]byte, error) {
	var buf []byte
	if x.Method != "" {
		buf = protowire.AppendVarint(buf, (1<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendString(buf, x.Method)
	}
	if len(x.Payload) > 0 {
		buf = protowire.AppendVarint(buf, (2<<3)|uint64(protowire.BytesType))
		buf = protowire.AppendBytes(buf, x.Payload)
	}
	if x.MsgId != 0 {
		buf = protowire.AppendVarint(buf, (3<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, uint64(x.MsgId))
	}
	if x.MsgType != 0 {
		buf = protowire.AppendVarint(buf, (4<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, uint64(x.MsgType))
	}
	if x.Offset != 0 {
		buf = protowire.AppendVarint(buf, (5<<3)|uint64(protowire.VarintType))
		buf = protowire.AppendVarint(buf, uint64(x.Offset))
	}
	return buf, nil
}

func (x *Message) Unmarshal(data []byte) error {
	*x = Message{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 3:
				x.MsgId = int64(v)
			case 4:
				x.MsgType = int64(v)
			case 5:
				x.Offset = int64(v)
			}
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 1:
				x.Method = string(v)
			case 2:
				x.Payload = v
			}
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

// ChatMessage — 弹幕消息
type ChatMessage struct {
	Common              *Common `json:"common"`
	User                *User   `json:"user"`
	Content             string  `json:"content"`
	VisibleToSender     bool    `json:"visibleToSender"`
	BackgroundImage     int64   `json:"backgroundImage"`
	FullScreenTextColor string  `json:"fullScreenTextColor"`
	BackgroundImageV2   int64   `json:"backgroundImageV2"`
}

func (x *ChatMessage) Unmarshal(data []byte) error {
	*x = ChatMessage{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 4:
				x.VisibleToSender = v != 0
			case 5:
				x.BackgroundImage = int64(v)
			case 7:
				x.BackgroundImageV2 = int64(v)
			}
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 1:
				msg := &Common{}
				if err := msg.Unmarshal(v); err != nil {
					return err
				}
				x.Common = msg
			case 2:
				msg := &User{}
				if err := msg.Unmarshal(v); err != nil {
					return err
				}
				x.User = msg
			case 3:
				x.Content = string(v)
			case 6:
				x.FullScreenTextColor = string(v)
			}
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

// Common — 消息元数据
type Common struct {
	Method     string `json:"method"`
	MsgId      int64  `json:"msgId"`
	RoomId     int64  `json:"roomId"`
	CreateTime int64  `json:"createTime"`
	Monitor    int64  `json:"monitor"`
	ShowEffect int64  `json:"showEffect"`
}

func (x *Common) Unmarshal(data []byte) error {
	*x = Common{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 2:
				x.MsgId = int64(v)
			case 3:
				x.RoomId = int64(v)
			case 4:
				x.CreateTime = int64(v)
			case 5:
				x.Monitor = int64(v)
			case 6:
				x.ShowEffect = int64(v)
			}
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 1:
				x.Method = string(v)
			}
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

// User — 用户信息
type User struct {
	Id       int64  `json:"id"`
	ShortId  int64  `json:"shortId"`
	Nickname string `json:"nickname"`
}

func (x *User) Unmarshal(data []byte) error {
	*x = User{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 1:
				x.Id = int64(v)
			case 2:
				x.ShortId = int64(v)
			}
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 3:
				x.Nickname = string(v)
			}
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

// GiftMessage — 礼物消息
type GiftMessage struct {
	Common      *Common `json:"common"`
	User        *User   `json:"user"`
	GiftId      int64   `json:"giftId"`
	RepeatCount int32   `json:"repeatCount"`
	ComboCount  int32   `json:"comboCount"`
	RepeatEnd   int32   `json:"repeatEnd"`
	Gift        *Gift   `json:"gift"`
}

func (x *GiftMessage) Unmarshal(data []byte) error {
	*x = GiftMessage{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 2:
				x.GiftId = int64(v)
			case 5:
				x.RepeatCount = int32(v)
			case 6:
				x.ComboCount = int32(v)
			case 9:
				x.RepeatEnd = int32(v)
			}
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 1:
				msg := &Common{}
				if err := msg.Unmarshal(v); err != nil {
					return err
				}
				x.Common = msg
			case 7:
				msg := &User{}
				if err := msg.Unmarshal(v); err != nil {
					return err
				}
				x.User = msg
			case 15:
				msg := &Gift{}
				if err := msg.Unmarshal(v); err != nil {
					return err
				}
				x.Gift = msg
			}
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

// Gift — 礼物详情
type Gift struct {
	Id           int64  `json:"id"`
	Name         string `json:"name"`
	DiamondCount int32  `json:"diamondCount"`
	Type         int32  `json:"type"`
	Describe     string `json:"describe"`
}

func (x *Gift) Unmarshal(data []byte) error {
	*x = Gift{}
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 5:
				x.Id = int64(v)
			case 11:
				x.Type = int32(v)
			case 12:
				x.DiamondCount = int32(v)
			}
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch num {
			case 2:
				x.Describe = string(v)
			case 16:
				x.Name = string(v)
			}
		default:
			n := protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}
