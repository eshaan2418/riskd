// Package deribit connects to Deribit's public market-data WebSocket (no API key, no account),
// records raw frames to a gzip JSONL journal, and decodes frames into engine events.
package deribit

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
)

// Endpoints (variables so tests can point them at a fake server).
var (
	WSURL   = "wss://www.deribit.com/ws/api/v2"
	RESTURL = "https://www.deribit.com/api/v2/public/get_book_summary_by_currency?currency=%s&kind=option"
)

// Line is one journal record: local receive time and the raw frame (WebSocket message or the
// REST book summary taken at start, which carries Deribit's per-expiry underlying prices).
type Line struct {
	RecvNs int64           `json:"recv_ns"`
	Kind   string          `json:"kind"` // "ws" or "rest"
	Msg    json.RawMessage `json:"msg"`
}

// Writer appends journal lines to a gzip file.
type Writer struct {
	f  *os.File
	gz *gzip.Writer
	bw *bufio.Writer
}

func Create(path string) (*Writer, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	gz, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	return &Writer{f: f, gz: gz, bw: bufio.NewWriterSize(gz, 1<<20)}, nil
}

func (w *Writer) Write(kind string, recvNs int64, msg []byte) error {
	if _, err := fmt.Fprintf(w.bw, `{"recv_ns":%d,"kind":%q,"msg":`, recvNs, kind); err != nil {
		return err
	}
	if _, err := w.bw.Write(msg); err != nil {
		return err
	}
	_, err := w.bw.WriteString("}\n")
	return err
}

func (w *Writer) Close() error {
	if err := w.bw.Flush(); err != nil {
		return err
	}
	if err := w.gz.Close(); err != nil {
		return err
	}
	return w.f.Close()
}

// Reader iterates journal lines.
type Reader struct {
	f  *os.File
	gz *gzip.Reader
	sc *bufio.Scanner
}

func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	return &Reader{f: f, gz: gz, sc: sc}, nil
}

// Next returns the next line; io.EOF at the end.
func (r *Reader) Next() (Line, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			return Line{}, err
		}
		return Line{}, io.EOF
	}
	var l Line
	err := json.Unmarshal(r.sc.Bytes(), &l)
	return l, err
}

func (r *Reader) Close() error { r.gz.Close(); return r.f.Close() }

// FetchSummary downloads the public option book summary for a currency.
func FetchSummary(ctx context.Context, currency string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(RESTURL, currency), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("deribit REST: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// Stream subscribes to the option mark-price and index channels and calls fn for every frame
// until ctx is done. It does not reconnect: a dropped connection ends the stream with an error,
// which keeps recordings gap-free by construction (a gap would be a new file).
func Stream(ctx context.Context, currency string, fn func(recvNs int64, msg []byte) error) error {
	c, _, err := websocket.Dial(ctx, WSURL, nil)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 26)
	lc := map[string]string{"BTC": "btc_usd", "ETH": "eth_usd"}[currency]
	sub := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"public/subscribe","params":{"channels":["markprice.options.%s","deribit_price_index.%s"]}}`, lc, lc)
	if err := c.Write(ctx, websocket.MessageText, []byte(sub)); err != nil {
		return err
	}
	// Heartbeats keep the session alive; Deribit sends test_request which must be answered.
	hb := `{"jsonrpc":"2.0","id":2,"method":"public/set_heartbeat","params":{"interval":30}}`
	if err := c.Write(ctx, websocket.MessageText, []byte(hb)); err != nil {
		return err
	}
	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			if ctx.Err() != nil || websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return err
		}
		now := time.Now().UnixNano()
		if isTestRequest(msg) {
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":3,"method":"public/test","params":{}}`))
		}
		if err := fn(now, msg); err != nil {
			return err
		}
	}
}

func isTestRequest(msg []byte) bool {
	var m struct {
		Method string `json:"method"`
		Params struct {
			Type string `json:"type"`
		} `json:"params"`
	}
	return json.Unmarshal(msg, &m) == nil && m.Method == "heartbeat" && m.Params.Type == "test_request"
}
