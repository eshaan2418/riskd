package deribit

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Kind of decoded event.
type Kind uint8

const (
	KindNone    Kind = iota // control frame (subscribe response, heartbeat): ignored
	KindMarks               // markprice.options.*: a batch of changed option marks
	KindIndex               // deribit_price_index.*: the spot index
	KindSummary             // REST book summary at start: every option's mark and Deribit's forward
)

// Mark is one option's mark: price in units of the underlying (BTC), Deribit's IV as a fraction.
type Mark struct {
	Name string  `json:"instrument_name"`
	Mark float64 `json:"mark_price"`
	IV   float64 `json:"iv"`
	TS   int64   `json:"timestamp"`
}

// SummaryRow is one REST book-summary row. MarkIV is converted from percent to a fraction.
type SummaryRow struct {
	Name       string  `json:"instrument_name"`
	Mark       float64 `json:"mark_price"`
	MarkIV     float64 `json:"mark_iv"`
	Underlying float64 `json:"underlying_price"`
	TS         int64   `json:"creation_timestamp"`
}

// Event is a decoded journal line. TS is the exchange timestamp in Unix milliseconds: the engine
// runs on exchange time so a replay reproduces a live run exactly.
type Event struct {
	Kind    Kind
	TS      int64
	RecvNs  int64
	Marks   []Mark
	Index   float64
	Summary []SummaryRow
}

type wsFrame struct {
	Method string `json:"method"`
	Params struct {
		Channel string          `json:"channel"`
		Data    json.RawMessage `json:"data"`
	} `json:"params"`
}

// Decode turns a journal line into an event.
func Decode(l Line) (Event, error) {
	ev := Event{RecvNs: l.RecvNs}
	if l.Kind == "rest" {
		var r struct {
			Result []SummaryRow `json:"result"`
		}
		if err := json.Unmarshal(l.Msg, &r); err != nil {
			return ev, fmt.Errorf("decode summary: %w", err)
		}
		ev.Kind = KindSummary
		ev.Summary = r.Result
		for i := range ev.Summary {
			ev.Summary[i].MarkIV /= 100
			ev.TS = max(ev.TS, ev.Summary[i].TS)
		}
		return ev, nil
	}
	return DecodeWS(l.RecvNs, l.Msg)
}

// DecodeWS decodes one WebSocket frame.
func DecodeWS(recvNs int64, msg []byte) (Event, error) {
	ev := Event{RecvNs: recvNs}
	var f wsFrame
	if err := json.Unmarshal(msg, &f); err != nil {
		return ev, fmt.Errorf("decode frame: %w", err)
	}
	if f.Method != "subscription" {
		return ev, nil
	}
	switch ch := f.Params.Channel; {
	case strings.HasPrefix(ch, "markprice.options."):
		if err := json.Unmarshal(f.Params.Data, &ev.Marks); err != nil {
			return ev, fmt.Errorf("decode marks: %w", err)
		}
		ev.Kind = KindMarks
		for _, m := range ev.Marks {
			ev.TS = max(ev.TS, m.TS)
		}
	case strings.HasPrefix(ch, "deribit_price_index."):
		var d struct {
			TS    int64   `json:"timestamp"`
			Price float64 `json:"price"`
		}
		if err := json.Unmarshal(f.Params.Data, &d); err != nil {
			return ev, fmt.Errorf("decode index: %w", err)
		}
		ev.Kind, ev.TS, ev.Index = KindIndex, d.TS, d.Price
	}
	return ev, nil
}
