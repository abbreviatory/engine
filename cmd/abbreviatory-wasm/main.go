//go:build js && wasm

// Command abbreviatory-wasm is the engine compiled to WebAssembly for the
// demo at abbreviatory.com/demo/. It runs in the visitor's browser: inputs
// are reduced there, kept there for expanding references, and compared with
// earlier runs there. Nothing is sent anywhere.
//
// It sets a global object, abbreviatory, with:
//
//	reduce(text, options) -> JSON string: the result as reduce -json gives it
//	expand(ref)           -> the original text, or throws
//	forget()              -> drops kept inputs and histories
//	version               -> the engine version
//
// options is a JSON string with format, budget, preserve, keep (an array),
// key and full, as the command line's flags.
package main

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"syscall/js"
	"time"

	abbreviatory "github.com/abbreviatory/engine"
)

type kept struct {
	data  []byte
	index *abbreviatory.Index
}

var (
	originals = map[string]kept{}
	histories = map[string][]abbreviatory.Run{}
)

// maxKept is how many inputs are kept for expanding; the oldest go first.
const maxKept = 20

var order []string

type options struct {
	Format   string   `json:"format"`
	Budget   int      `json:"budget"`
	Preserve *string  `json:"preserve"`
	Keep     []string `json:"keep"`
	Key      string   `json:"key"`
	Full     bool     `json:"full"`
}

func reduce(text string, raw string) (map[string]any, error) {
	var o options
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			return nil, err
		}
	}
	opt := abbreviatory.Options{Format: o.Format, Budget: o.Budget, Full: o.Full}
	if opt.Format == "" {
		opt.Format = "auto"
	}
	if err := abbreviatory.CheckFormat(opt.Format); err != nil {
		return nil, err
	}
	if o.Preserve != nil {
		opt.Preserve = []string{}
		for _, p := range strings.Split(*o.Preserve, ",") {
			if p = strings.TrimSpace(p); p != "" {
				opt.Preserve = append(opt.Preserve, p)
			}
		}
	}
	for _, k := range o.Keep {
		if k == "" {
			continue
		}
		re, err := regexp.Compile(k)
		if err != nil {
			return nil, errors.New("keep: " + err.Error())
		}
		opt.Keep = append(opt.Keep, re)
	}
	if o.Key != "" {
		opt.Track = true
		opt.History = histories[o.Key]
	}
	data := []byte(text)
	start := time.Now()
	res, err := abbreviatory.Reduce(strings.NewReader(text), opt)
	if err != nil {
		return nil, err
	}
	took := time.Since(start)
	if _, ok := originals[res.ID]; !ok {
		order = append(order, res.ID)
		if len(order) > maxKept {
			delete(originals, order[0])
			order = order[1:]
		}
	}
	originals[res.ID] = kept{data, res.Index}
	out := map[string]any{
		"version":     abbreviatory.Version,
		"format":      res.Format,
		"family":      res.Family,
		"ref":         res.ID,
		"input_bytes": res.InputBytes,
		"input_lines": res.InputLines,
		"overflow":    res.Overflow,
		"records":     res.Records,
		"lines":       res.Lines,
		"text":        res.Text,
		"ms":          float64(took.Microseconds()) / 1000,
	}
	if res.Run != nil {
		res.Run.Time = time.Now().UTC().Format(time.RFC3339)
		h := append(histories[o.Key], *res.Run)
		if len(h) > abbreviatory.FlakyWindow {
			h[len(h)-abbreviatory.FlakyWindow-1].Trim()
		}
		histories[o.Key] = h
		out["run"] = res.Run.N
		out["unchanged"] = res.Delta.Unchanged
		out["new"] = len(res.Delta.New)
		out["fixed"] = len(res.Delta.Fixed)
	}
	return out, nil
}

func expand(ref string) ([]byte, error) {
	id, sel, err := abbreviatory.ParseRef(ref)
	if err != nil {
		return nil, err
	}
	k, ok := originals[id]
	if !ok {
		return nil, errors.New(id + " is no longer kept in this page; reduce the input again")
	}
	return abbreviatory.Expand(k.data, k.index, sel)
}

func throw(err error) any {
	return js.Global().Get("Error").New(err.Error())
}

func main() {
	api := js.Global().Get("Object").New()
	api.Set("version", abbreviatory.Version)
	api.Set("reduce", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 1 {
			return throw(errors.New("reduce(text, options)"))
		}
		opts := ""
		if len(args) > 1 && args[1].Type() == js.TypeString {
			opts = args[1].String()
		}
		out, err := reduce(args[0].String(), opts)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		b, _ := json.Marshal(out)
		return string(b)
	}))
	api.Set("expand", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) < 1 {
			return map[string]any{"error": "expand(ref)"}
		}
		data, err := expand(args[0].String())
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return string(data)
	}))
	api.Set("forget", js.FuncOf(func(this js.Value, args []js.Value) any {
		originals, histories, order = map[string]kept{}, map[string][]abbreviatory.Run{}, nil
		return nil
	}))
	js.Global().Set("abbreviatory", api)
	if cb := js.Global().Get("abbreviatoryReady"); cb.Type() == js.TypeFunction {
		cb.Invoke()
	}
	select {}
}
