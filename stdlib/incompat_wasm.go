//go:build wasm

package stdlib

// Wasm-only incompatibilities: these pass natively, so gate on GOARCH.
func init() {
	m := Incompat["encoding/gob"]
	if m == nil {
		m = map[string]string{}
		Incompat["encoding/gob"] = m
	}
	m["TestLargeSlice"] = "four parallel multi-MB slice encode/decodes exceed wasm's 4GB linear memory under the interpreter"
	m["TestCountEncodeMallocs"] = "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0"
	m["TestCountDecodeMallocs"] = "testing.AllocsPerRun observes mvm interpreter allocations; native expects 3"

	Incompat["mime"] = map[string]string{
		"TestLookupMallocs": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
	}

	m = Incompat["path/filepath"]
	if m == nil {
		m = map[string]string{}
		Incompat["path/filepath"] = m
	}
	m["TestClean"] = "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0 (check gated on GOMAXPROCS==1, always true on wasm)"

	Incompat["compress/flate"] = map[string]string{
		"TestDeflateFast_Reset": "table-wraparound reset writes a ~2.5MB corpus 3x across many resets; 92s interpreted on wasm (short path still slow)",
		"TestDeterministic":     "encodes a compressible stream twice at 11 levels in parallel; 83s interpreted on wasm (short path still slow)",
		"TestMaxStackSize":      "compresses 1MB at every level in parallel goroutines; 31s interpreted on wasm (no testing.Short path)",
	}

	Incompat["mime/multipart"] = map[string]string{
		"TestReadForm_MetadataTooLarge": "pushes a 10MiB field name / 10MiB header / 110000 parts through ReadForm to hit ErrMessageTooLarge; 80s interpreted on wasm (no testing.Short path)",
		"TestMultipartSlowInput":        "feeds a large multipart body one byte at a time (slowReader) through NextPart/io.Copy; 61s interpreted on wasm (no testing.Short path)",
		"TestReadFormEndlessHeaderLine": "reads a neverending header stream until ReadForm's 1MiB limit for 3 header shapes; 40s interpreted on wasm (no testing.Short path)",
	}

	Incompat["image/png"] = map[string]string{
		"TestWriteRGBA": "builds four 640x480 RGBA images per-pixel then encode/decode/diffs each; 114s interpreted on wasm (no testing.Short path)",
	}

	Incompat["image/gif"] = map[string]string{
		"TestEncodeWrappedImage":               "encodes a wrapped non-paletted image.Image (quantization) + per-pixel averageDelta; 47s interpreted on wasm (no testing.Short path)",
		"TestWriter":                           "round-trips testdata images through Encode/Decode + per-pixel averageDelta; 29s interpreted on wasm (no testing.Short path)",
		"TestEncodeAllGo1Dot4":                 "EncodeAll a multi-frame GIF from testdata frames then DecodeAll+compare; 17s interpreted on wasm (no testing.Short path)",
		"TestEncodeAllGo1Dot5":                 "EncodeAll a multi-frame GIF from testdata frames then DecodeAll+compare; 17s interpreted on wasm (no testing.Short path)",
		"TestEncodeAllGo1Dot5GlobalColorModel": "EncodeAll a multi-frame GIF from testdata frames then DecodeAll+compare; 20s interpreted on wasm (no testing.Short path)",
		"TestDecodeMemoryConsumption":          "EncodeAll+Decode a 3000-frame GIF; 14s interpreted on wasm (no testing.Short path)",
	}

	Incompat["image/jpeg"] = map[string]string{
		"TestDCT":         "validates FDCT/IDCT against big.Float reference DCTs (testSlowVsBig) over ~230 blocks; 61s interpreted on wasm",
		"TestEncodeYCbCr": "builds a 640x480 image per-pixel then encodes it twice; 25s interpreted on wasm",
	}

	Incompat["regexp"] = map[string]string{
		"TestBadCompile": "too long under interpreter in wasm",
	}

	Incompat["net/textproto"] = map[string]string{
		"TestCommonHeaders": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
	}

	Incompat["net/netip"] = map[string]string{
		"TestParsePrefixAllocs": "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestNoAllocs":          "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0",
		"TestAddrStringAllocs":  "testing.AllocsPerRun observes mvm interpreter allocations; native expects 0/1",
	}
}
