package deribit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeFrames(t *testing.T) {
	ev, err := DecodeWS(1, []byte(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"markprice.options.btc_usd","data":[{"timestamp":1793606920789,"iv":0.3766,"instrument_name":"BTC-29JAN27-102000-C","mark_price":0.0082},{"timestamp":1793606920790,"iv":0.47,"instrument_name":"BTC-27NOV26-105000-P","mark_price":0.34}]}}`))
	if err != nil || ev.Kind != KindMarks || len(ev.Marks) != 2 || ev.TS != 1793606920790 || ev.Marks[0].IV != 0.3766 {
		t.Fatalf("%+v %v", ev, err)
	}
	ev, err = DecodeWS(1, []byte(`{"jsonrpc":"2.0","method":"subscription","params":{"channel":"deribit_price_index.btc_usd","data":{"timestamp":1793606920006,"price":78084.19,"index_name":"btc_usd"}}}`))
	if err != nil || ev.Kind != KindIndex || ev.Index != 78084.19 || ev.TS != 1793606920006 {
		t.Fatalf("%+v %v", ev, err)
	}
	ev, err = DecodeWS(1, []byte(`{"jsonrpc":"2.0","id":1,"result":["markprice.options.btc_usd"]}`))
	if err != nil || ev.Kind != KindNone {
		t.Fatalf("%+v %v", ev, err)
	}
	if _, err := DecodeWS(1, []byte(`{"method":"subscription","params":{"channel":"markprice.options.btc_usd","data":{"x":1}}}`)); err == nil {
		t.Fatal("expected error for malformed marks")
	}
	ev, err = Decode(Line{Kind: "rest", Msg: []byte(`{"result":[{"instrument_name":"BTC-3NOV26-78000-C","mark_price":0.01,"mark_iv":35.5,"underlying_price":78010.5,"creation_timestamp":5}]}`)})
	if err != nil || ev.Kind != KindSummary || ev.Summary[0].MarkIV != 0.355 || ev.TS != 5 {
		t.Fatalf("%+v %v", ev, err)
	}
}

func TestJournalRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j.jsonl.gz")
	w, err := Create(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Write("rest", 1, []byte(`{"result":[]}`))
	_ = w.Write("ws", 2, []byte(`{"a":1}`))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	a, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != "rest" || a.RecvNs != 1 || string(b.Msg) != `{"a":1}` || b.RecvNs != 2 {
		t.Fatalf("%+v %+v", a, b)
	}
	if _, err := r.Next(); err == nil {
		t.Fatal("want EOF")
	}
	_ = os.Remove(p)
}

func FuzzDecodeWS(f *testing.F) {
	f.Add([]byte(`{"method":"subscription","params":{"channel":"markprice.options.btc_usd","data":[{"timestamp":1,"iv":0.3,"instrument_name":"BTC-29JAN27-102000-C","mark_price":0.0082}]}}`))
	f.Add([]byte(`{"method":"subscription","params":{"channel":"deribit_price_index.btc_usd","data":{"timestamp":1,"price":1}}}`))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = DecodeWS(0, b) })
}
