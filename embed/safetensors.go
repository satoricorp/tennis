package embed

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// safetensors is a deliberately boring container: an 8-byte little-endian
// header length, that many bytes of JSON describing every tensor, then the raw
// tensor bytes back to back. A model2vec model is a single 2-D float matrix, so
// this reader stops well short of the full spec — it finds the one matrix and
// refuses anything else rather than pretending to be a general loader.

type tensorInfo struct {
	DType   string   `json:"dtype"`
	Shape   []int    `json:"shape"`
	Offsets [2]int64 `json:"data_offsets"`
}

// Matrix is a row-major rows×cols float32 matrix kept in one flat slice, so
// looking up a token's vector is a reslice rather than an allocation. At
// 63k×512 that is ~129MB resident, which is the price of admission for having
// no inference engine at all.
type Matrix struct {
	Rows, Cols int
	data       []float32
}

// Row returns a view into the backing array. Callers must not retain or mutate
// it; every caller here reads it immediately into an accumulator.
func (m *Matrix) Row(i int) []float32 { return m.data[i*m.Cols : (i+1)*m.Cols] }

// readSafetensors loads the single 2-D F32 tensor from path. Anything else —
// no tensors, several tensors, a non-float dtype, a non-2-D shape — is an
// error, because silently picking the "wrong" matrix would surface much later
// as embeddings that are merely bad rather than obviously broken.
func readSafetensors(path string) (*Matrix, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) < 8 {
		return nil, fmt.Errorf("safetensors: file is %d bytes, too short for a header", len(raw))
	}
	hdrLen := binary.LittleEndian.Uint64(raw[:8])
	if hdrLen == 0 || uint64(len(raw)) < 8+hdrLen {
		return nil, fmt.Errorf("safetensors: header length %d overruns a %d-byte file", hdrLen, len(raw))
	}

	var header map[string]json.RawMessage
	if err := json.Unmarshal(raw[8:8+hdrLen], &header); err != nil {
		return nil, fmt.Errorf("safetensors: bad JSON header: %w", err)
	}

	var (
		found bool
		info  tensorInfo
		name  string
	)
	for k, v := range header {
		if k == "__metadata__" {
			continue
		}
		var ti tensorInfo
		if err := json.Unmarshal(v, &ti); err != nil {
			continue
		}
		if len(ti.Shape) != 2 {
			continue
		}
		if found {
			return nil, fmt.Errorf("safetensors: found both %q and %q as 2-D tensors; expected exactly one", name, k)
		}
		found, info, name = true, ti, k
	}
	if !found {
		return nil, fmt.Errorf("safetensors: no 2-D tensor in %s", path)
	}
	if info.DType != "F32" {
		return nil, fmt.Errorf("safetensors: tensor %q is %s; only F32 is supported", name, info.DType)
	}

	body := raw[8+hdrLen:]
	start, end := info.Offsets[0], info.Offsets[1]
	if start < 0 || end > int64(len(body)) || start > end {
		return nil, fmt.Errorf("safetensors: tensor %q offsets [%d,%d) fall outside a %d-byte body", name, start, end, len(body))
	}
	blob := body[start:end]
	rows, cols := info.Shape[0], info.Shape[1]
	if want := rows * cols * 4; len(blob) != want {
		return nil, fmt.Errorf("safetensors: tensor %q is %d bytes but shape %dx%d needs %d", name, len(blob), rows, cols, want)
	}

	data := make([]float32, rows*cols)
	for i := range data {
		data[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return &Matrix{Rows: rows, Cols: cols, data: data}, nil
}
