package artifact

import "encoding/binary"

// Java class files share CAFEBABE with universal Mach-O. Recognize the complete
// class-file envelope, not just a plausible version or a .class extension: a
// truncated or otherwise malformed fat binary must still fail Mach-O parsing.
// This checks framing and the indices needed for that framing, not JVM bytecode.
// https://docs.oracle.com/javase/specs/jvms/se25/html/jvms-4.html
func isJavaClass(data []byte) bool {
	if len(data) < 10 || binary.BigEndian.Uint32(data) != 0xcafebabe {
		return false
	}
	r := classReader{data: data[4:], ok: true}
	minor, major := r.u2(), r.u2()
	if major < 45 || major >= 56 && minor != 0 && minor != 65535 {
		return false
	}
	count := int(r.u2())
	if count < 2 {
		return false
	}
	tags := make([]byte, count)
	for i := 1; i < count; i++ {
		tag := r.u1()
		tags[i] = tag
		switch tag {
		case 1: // CONSTANT_Utf8
			r.skip(int(r.u2()))
		case 3, 4, 9, 10, 11, 12, 17, 18:
			r.skip(4)
		case 5, 6: // Long and Double occupy two constant-pool slots.
			r.skip(8)
			i++
			if i >= count {
				return false
			}
		case 7, 8, 16, 19, 20:
			r.skip(2)
		case 15:
			r.skip(3)
		default:
			return false
		}
		if !r.ok {
			return false
		}
	}
	isTag := func(index uint16, tag byte) bool {
		return index > 0 && int(index) < len(tags) && tags[index] == tag
	}
	r.skip(2) // access_flags
	if !isTag(r.u2(), 7) {
		return false
	}
	if super := r.u2(); super != 0 && !isTag(super, 7) {
		return false
	}
	for n := int(r.u2()); n > 0; n-- {
		if !isTag(r.u2(), 7) {
			return false
		}
	}
	attributes := func() bool {
		for n := int(r.u2()); n > 0; n-- {
			if !isTag(r.u2(), 1) {
				return false
			}
			length := r.u4()
			if uint64(length) > uint64(len(r.data)) {
				return false
			}
			r.skip(int(length))
		}
		return r.ok
	}
	for section := 0; section < 2; section++ { // fields, methods
		for n := int(r.u2()); n > 0; n-- {
			r.skip(2) // access_flags
			if !isTag(r.u2(), 1) || !isTag(r.u2(), 1) || !attributes() {
				return false
			}
		}
	}
	return attributes() && r.ok && len(r.data) == 0
}

type classReader struct {
	data []byte
	ok   bool
}

func (r *classReader) take(n int) []byte {
	if !r.ok || n > len(r.data) {
		r.ok = false
		return nil
	}
	value := r.data[:n]
	r.data = r.data[n:]
	return value
}
func (r *classReader) skip(n int) { r.take(n) }
func (r *classReader) u1() byte {
	if p := r.take(1); p != nil {
		return p[0]
	}
	return 0
}
func (r *classReader) u2() uint16 {
	if p := r.take(2); p != nil {
		return binary.BigEndian.Uint16(p)
	}
	return 0
}
func (r *classReader) u4() uint32 {
	if p := r.take(4); p != nil {
		return binary.BigEndian.Uint32(p)
	}
	return 0
}
