package sandbox

import (
	"fmt"
)

// WebAssembly section identifiers used by the auditor.
const (
	sectionImport = 2
	sectionMemory = 5
	sectionExport = 7
	sectionStart  = 8
)

// maxVecEntries rejects absurd element counts before they can drive allocation.
const maxVecEntries = 1 << 20

// cursor walks a bounded byte slice, decoding the little-endian and LEB128
// primitives of the wasm binary format. Every read is bounds-checked: a hostile
// artifact can force an error but never a panic or an out-of-range access.
type cursor struct {
	buf   []byte
	index int
}

func (c *cursor) byte() (byte, error) {
	if c.index >= len(c.buf) {
		return 0, fmt.Errorf("unexpected end of section at %d", c.index)
	}
	b := c.buf[c.index]
	c.index++
	return b, nil
}

// u32 decodes an unsigned LEB128 value, rejecting overlong encodings.
func (c *cursor) u32() (uint32, error) {
	var result uint64
	var shift uint
	for i := 0; i < 5; i++ {
		b, err := c.byte()
		if err != nil {
			return 0, err
		}
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			if i == 4 && b&0x78 != 0 {
				return 0, fmt.Errorf("leb128 value does not fit in uint32")
			}
			if result > 1<<32-1 {
				return 0, fmt.Errorf("leb128 value overflows uint32")
			}
			return uint32(result), nil
		}
		shift += 7
	}
	return 0, fmt.Errorf("leb128 encoding too long")
}

// name decodes a length-prefixed UTF-8 string.
func (c *cursor) name() (string, error) {
	length, err := c.u32()
	if err != nil {
		return "", err
	}
	if uint64(length) > uint64(len(c.buf)-c.index) {
		return "", fmt.Errorf("length-prefixed name overruns section")
	}
	s := string(c.buf[c.index : c.index+int(length)])
	c.index += int(length)
	return s, nil
}

// vecLen decodes a vector length and refuses implausibly large counts.
func (c *cursor) vecLen() (uint32, error) {
	n, err := c.u32()
	if err != nil {
		return 0, err
	}
	if n > maxVecEntries {
		return 0, fmt.Errorf("vector length %d exceeds %d", n, maxVecEntries)
	}
	return n, nil
}

// limits decodes a wasm memory/table limits field: flag, min and optional max.
func (c *cursor) limits() (min, max uint32, hasMax bool, err error) {
	flag, err := c.byte()
	if err != nil {
		return 0, 0, false, err
	}
	min, err = c.u32()
	if err != nil {
		return 0, 0, false, err
	}
	if flag&0x01 != 0 {
		hasMax = true
		if max, err = c.u32(); err != nil {
			return 0, 0, false, err
		}
		if hasMax && max < min {
			return 0, 0, false, fmt.Errorf("maximum %d below minimum %d", max, min)
		}
	}
	return min, max, hasMax, nil
}

// sectionReader iterates the top-level sections of a module body.
type sectionReader struct {
	buf    []byte
	offset int
	body   []byte
}

// readSectionHeader advances to the next section and returns its id.
func (r *sectionReader) readSectionHeader() (byte, error) {
	c := &cursor{buf: r.buf, index: r.offset}
	id, err := c.u32()
	if err != nil {
		return 0, err
	}
	size, err := c.u32()
	if err != nil {
		return 0, err
	}
	remaining := len(r.buf) - c.index
	if uint64(size) > uint64(remaining) {
		return 0, fmt.Errorf("section %d declares %d bytes, only %d remain", id, size, remaining)
	}
	r.body = r.buf[c.index : c.index+int(size)]
	r.offset = c.index + int(size)
	return byte(id), nil
}

func (r *sectionReader) sectionBody() []byte { return r.body }

// decodeImports returns every import declaration in an import section body.
func decodeImports(body []byte) ([]ImportRef, error) {
	c := &cursor{buf: body}
	count, err := c.vecLen()
	if err != nil {
		return nil, err
	}

	refs := make([]ImportRef, 0, count)
	for i := uint32(0); i < count; i++ {
		moduleName, err := c.name()
		if err != nil {
			return nil, err
		}
		fieldName, err := c.name()
		if err != nil {
			return nil, err
		}
		kind, err := c.byte()
		if err != nil {
			return nil, err
		}

		switch kind {
		case 0x00: // function: type index
			if _, err = c.u32(); err != nil {
				return nil, err
			}
		case 0x01: // table: element type + limits
			if _, err = c.byte(); err != nil {
				return nil, err
			}
			if _, _, _, err = c.limits(); err != nil {
				return nil, err
			}
		case 0x02: // memory: limits
			if _, _, _, err = c.limits(); err != nil {
				return nil, err
			}
		case 0x03: // global: value type + mutability
			if _, err = c.byte(); err != nil {
				return nil, err
			}
			if _, err = c.byte(); err != nil {
				return nil, err
			}
		case 0x04: // tag: attribute + type index
			if _, err = c.byte(); err != nil {
				return nil, err
			}
			if _, err = c.u32(); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unknown import kind 0x%02x", kind)
		}

		refs = append(refs, ImportRef{
			Module: moduleName,
			Name:   fieldName,
			Kind:   importKindName(kind),
		})
	}
	return refs, nil
}

// decodeMemorySection reports the largest memory declaration in a memory
// section body.
func decodeMemorySection(body []byte) (min, max uint32, hasMax bool, err error) {
	c := &cursor{buf: body}
	count, err := c.vecLen()
	if err != nil {
		return 0, 0, false, err
	}
	for i := uint32(0); i < count; i++ {
		mn, mx, bounded, err := c.limits()
		if err != nil {
			return 0, 0, false, err
		}
		if mn > min {
			min, max, hasMax = mn, mx, bounded
		}
	}
	return min, max, hasMax, nil
}

// decodeExports returns export names in an export section body.
func decodeExports(body []byte) ([]string, error) {
	c := &cursor{buf: body}
	count, err := c.vecLen()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		name, err := c.name()
		if err != nil {
			return nil, err
		}
		if _, err = c.byte(); err != nil { // kind
			return nil, err
		}
		if _, err = c.u32(); err != nil { // index
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

func importKindName(kind byte) string {
	switch kind {
	case 0x00:
		return "func"
	case 0x01:
		return "table"
	case 0x02:
		return "memory"
	case 0x03:
		return "global"
	case 0x04:
		return "tag"
	}
	return fmt.Sprintf("unknown(0x%02x)", kind)
}
