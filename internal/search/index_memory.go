package search

import (
	"reflect"
	"unsafe"

	"github.com/townsendmerino/aikit/bm25"
	"github.com/townsendmerino/aikit/chunk"
	"github.com/townsendmerino/ken/internal/structural"
)

// FormulaChunkText computes byte size for a slice of chunks:
// backing text string bytes + Chunk struct array.
func FormulaChunkText(chunks []chunk.Chunk) uint64 {
	var total uint64
	for i := range chunks {
		total += uint64(len(chunks[i].Text))
	}
	total += uint64(len(chunks)) * uint64(unsafe.Sizeof(chunk.Chunk{}))
	return total
}

// FormulaBM25 computes byte size of a built bm25.Index from live data structures.
func FormulaBM25(ix *bm25.Index) uint64 {
	if ix == nil {
		return 0
	}
	v := reflect.ValueOf(ix).Elem()
	var total uint64

	// docLen []int (8 bytes per element)
	docLen := v.FieldByName("docLen")
	if docLen.IsValid() {
		total += uint64(docLen.Len()) * 8
	}

	// norm []float64 (8 bytes per element)
	norm := v.FieldByName("norm")
	if norm.IsValid() {
		total += uint64(norm.Len()) * 8
	}

	// postings []posting (8 bytes per element in v1.71.0+)
	postings := v.FieldByName("postings")
	if postings.IsValid() && postings.Kind() == reflect.Slice {
		total += uint64(postings.Len()) * 8
	}

	// entries []termEntry
	entries := v.FieldByName("entries")
	if entries.IsValid() && entries.Kind() == reflect.Slice {
		if entries.Len() > 0 {
			elemSize := entries.Index(0).Type().Size()
			total += uint64(entries.Len()) * uint64(elemSize)

			// If v1.70.1 style where each termEntry holds postings slice:
			for i := 0; i < entries.Len(); i++ {
				e := entries.Index(i)
				p := e.FieldByName("postings")
				if p.IsValid() && p.Kind() == reflect.Slice {
					total += uint64(p.Len()) * 8
				}
			}
		}
	}

	// terms map[string]int32
	terms := v.FieldByName("terms")
	if terms.IsValid() && terms.Kind() == reflect.Map {
		numTerms := terms.Len()
		total += MapOverhead(numTerms)
		iter := terms.MapRange()
		for iter.Next() {
			k := iter.Key().String()
			total += uint64(len(k))
		}
	}

	return total
}

// FormulaRetainedF32 computes byte size of vecs [][]float32 in WatchedIndex.
func FormulaRetainedF32(vecs [][]float32) uint64 {
	if len(vecs) == 0 {
		return 0
	}
	dim := len(vecs[0])
	return uint64(len(vecs))*(uint64(dim)*4) + uint64(len(vecs))*24
}

// FormulaFlatI8 computes byte size of FlatI8: bq [N*dim]int8 + scales [N]float32.
func FormulaFlatI8(n, dim int) uint64 {
	return uint64(n)*uint64(dim) + uint64(n)*4 + 64
}

// FormulaFlatBinaryI8 computes byte size of FlatBinaryI8: codes [N*words*8] + center + FlatI8.
func FormulaFlatBinaryI8(n, dim int) uint64 {
	words := (dim + 63) / 64
	pfCodes := uint64(n) * uint64(words) * 8
	center := uint64(dim) * 4
	return pfCodes + center + FormulaFlatI8(n, dim)
}

// MapOverhead returns Go map bucket and struct overhead for n entries.
func MapOverhead(n int) uint64 {
	if n == 0 {
		return 48
	}
	b := 0
	for float64(uint64(1<<b))*6.5 < float64(n) {
		b++
	}
	buckets := uint64(1 << b)
	return 48 + buckets*208
}

// FormulaStructural computes byte size of structural.Index.
func FormulaStructural(ix *structural.Index) uint64 {
	if ix == nil {
		return 0
	}
	v := reflect.ValueOf(ix).Elem()
	var total uint64
	total += uint64(unsafe.Sizeof(structural.Index{}))

	files := v.FieldByName("files")
	if files.IsValid() && files.Kind() == reflect.Map {
		total += MapOverhead(files.Len())
		iter := files.MapRange()
		for iter.Next() {
			k := iter.Key().String()
			total += uint64(len(k))
			fsVal := iter.Value()
			if !fsVal.IsNil() {
				fs := fsVal.Elem()
				total += uint64(unsafe.Sizeof(structural.FileStruct{}))
				p := fs.FieldByName("Path").String()
				total += uint64(len(p))
				pkg := fs.FieldByName("Package").String()
				total += uint64(len(pkg))

				// Functions []FuncDef
				fn := fs.FieldByName("Functions")
				if fn.IsValid() {
					total += uint64(fn.Cap()) * uint64(unsafe.Sizeof(structural.FuncDef{}))
					for i := 0; i < fn.Len(); i++ {
						fd := fn.Index(i)
						total += uint64(len(fd.FieldByName("Name").String()))
						total += uint64(len(fd.FieldByName("ReturnType").String()))
						total += uint64(len(fd.FieldByName("EnclosingClass").String()))
						params := fd.FieldByName("Params")
						if params.IsValid() && params.Len() > 0 {
							total += uint64(params.Cap())*16 + 24
							for j := 0; j < params.Len(); j++ {
								total += uint64(len(params.Index(j).String()))
							}
						}
					}
				}
				// Classes []ClassDef
				cl := fs.FieldByName("Classes")
				if cl.IsValid() {
					total += uint64(cl.Cap()) * uint64(unsafe.Sizeof(structural.ClassDef{}))
					for i := 0; i < cl.Len(); i++ {
						cd := cl.Index(i)
						total += uint64(len(cd.FieldByName("Name").String()))
						m := cd.FieldByName("Methods")
						if m.IsValid() {
							total += uint64(m.Cap()) * uint64(unsafe.Sizeof(structural.FuncDef{}))
							for j := 0; j < m.Len(); j++ {
								md := m.Index(j)
								total += uint64(len(md.FieldByName("Name").String()))
							}
						}
					}
				}
				// CallRefs []CallRef
				cr := fs.FieldByName("CallRefs")
				if cr.IsValid() {
					total += uint64(cr.Cap()) * uint64(unsafe.Sizeof(structural.CallRef{}))
					for i := 0; i < cr.Len(); i++ {
						ref := cr.Index(i)
						total += uint64(len(ref.FieldByName("Callee").String()))
						total += uint64(len(ref.FieldByName("Receiver").String()))
						total += uint64(len(ref.FieldByName("EnclosingSymbol").String()))
					}
				}
				// Imports []string
				imp := fs.FieldByName("Imports")
				if imp.IsValid() && imp.Len() > 0 {
					total += uint64(imp.Cap())*16 + 24
					for i := 0; i < imp.Len(); i++ {
						total += uint64(len(imp.Index(i).String()))
					}
				}
				// Raises []string
				raises := fs.FieldByName("Raises")
				if raises.IsValid() && raises.Len() > 0 {
					total += uint64(raises.Cap())*16 + 24
					for i := 0; i < raises.Len(); i++ {
						total += uint64(len(raises.Index(i).String()))
					}
				}
			}
		}
	}

	for _, fieldName := range []string{"callers", "defs", "methods"} {
		f := v.FieldByName(fieldName)
		if f.IsValid() && f.Kind() == reflect.Map {
			total += MapOverhead(f.Len())
			iter := f.MapRange()
			for iter.Next() {
				k := iter.Key().String()
				total += uint64(len(k))
				s := iter.Value()
				if s.Kind() == reflect.Slice {
					elemSize := s.Type().Elem().Size()
					total += uint64(s.Cap())*uint64(elemSize) + 24
					if fieldName == "callers" {
						for i := 0; i < s.Len(); i++ {
							cs := s.Index(i)
							total += uint64(len(cs.FieldByName("File").String()))
						}
					} else {
						for i := 0; i < s.Len(); i++ {
							total += uint64(len(s.Index(i).String()))
						}
					}
				}
			}
		}
	}
	return total
}
