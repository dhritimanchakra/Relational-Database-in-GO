package main

import (
	"bytes"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"unsafe"
)

type memTree struct {
	tree  BTree
	ref   map[string]string
	pages map[uint64]BNode
}

func newMemTree() *memTree {
	pages := map[uint64]BNode{}
	return &memTree{
		tree: BTree{
			get: func(ptr uint64) []byte {
				node, ok := pages[ptr]
				assert(ok)
				return node
			},
			new: func(node []byte) uint64 {
				assert(BNode(node).nbytes() <= BTREE_PAGE_SIZE)
				ptr := uint64(uintptr(unsafe.Pointer(&node[0])))
				assert(pages[ptr] == nil)
				pages[ptr] = node
				return ptr
			},
			del: func(ptr uint64) {
				assert(pages[ptr] != nil)
				delete(pages, ptr)
			},
		},
		ref:   map[string]string{},
		pages: pages,
	}
}

func (m *memTree) set(t *testing.T, k, v string) {
	t.Helper()
	if err := m.tree.Insert([]byte(k), []byte(v)); err != nil {
		t.Fatalf("insert %q: %v", k, err)
	}
	m.ref[k] = v
}

func (m *memTree) del(t *testing.T, k string) {
	t.Helper()
	deleted, err := m.tree.Delete([]byte(k))
	if err != nil {
		t.Fatalf("delete %q: %v", k, err)
	}
	_, existed := m.ref[k]
	if deleted != existed {
		t.Fatalf("delete %q: got deleted=%v, want %v", k, deleted, existed)
	}
	delete(m.ref, k)
}

func (m *memTree) verify(t *testing.T) {
	t.Helper()
	if m.tree.root == 0 {
		return
	}
	var walk func(node BNode)
	walk = func(node BNode) {
		if int(node.nbytes()) > BTREE_PAGE_SIZE {
			t.Fatalf("node larger than a page: %d bytes", node.nbytes())
		}
		for i := uint16(1); i < node.nkeys(); i++ {
			if bytes.Compare(node.getKey(i-1), node.getKey(i)) >= 0 {
				t.Fatalf("keys not strictly ascending in node")
			}
		}
		if node.btype() == BNODE_NODE {
			for i := uint16(0); i < node.nkeys(); i++ {
				child := BNode(m.tree.get(node.getPtr(i)))
				if !bytes.Equal(node.getKey(i), child.getKey(0)) {
					t.Fatalf("separator key != child's first key")
				}
				walk(child)
			}
		}
	}
	walk(BNode(m.tree.get(m.tree.root)))
}

func (m *memTree) check(t *testing.T) {
	t.Helper()
	m.verify(t)
	for k, v := range m.ref {
		got, ok := m.tree.Get([]byte(k))
		if !ok || string(got) != v {
			t.Fatalf("Get(%q) = %q,%v want %q", k, got, ok, v)
		}
	}
	keys := make([]string, 0, len(m.ref))
	for k := range m.ref {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var scanned []string
	m.tree.Scan(nil, func(k, v []byte) bool {
		scanned = append(scanned, string(k))
		if m.ref[string(k)] != string(v) {
			t.Fatalf("scan value mismatch for %q", k)
		}
		return true
	})
	if strings.Join(scanned, "\x00") != strings.Join(keys, "\x00") {
		t.Fatalf("full scan mismatch: got %d keys, want %d", len(scanned), len(keys))
	}
	if len(keys) > 0 {
		start := keys[len(keys)/2]
		var from []string
		m.tree.Scan([]byte(start), func(k, _ []byte) bool { from = append(from, string(k)); return true })
		want := keys[sort.SearchStrings(keys, start):]
		if strings.Join(from, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("scan from %q mismatch", start)
		}
	}
}

func TestBTreeRandomOps(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	m := newMemTree()
	for i := 0; i < 30000; i++ {
		k := fmt.Sprintf("key-%05d", rng.Intn(4000))
		if rng.Intn(3) < 2 {
			m.set(t, k, strings.Repeat("v", rng.Intn(700)))
		} else {
			m.del(t, k)
		}
		if i%2500 == 0 {
			m.check(t)
		}
	}
	m.check(t)
}

func TestBTreeLargeKeysAndValues(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	m := newMemTree()
	for i := 0; i < 600; i++ {
		k := fmt.Sprintf("%04d-", rng.Intn(300)) + strings.Repeat("k", rng.Intn(900))
		m.set(t, k, strings.Repeat("v", rng.Intn(BTREE_MAX_VAL_SIZE+1)))
		if i%7 == 0 {
			m.del(t, k)
		}
	}
	m.check(t)
}

func TestBTreeDeleteEverything(t *testing.T) {
	m := newMemTree()
	for i := 0; i < 3000; i++ {
		m.set(t, fmt.Sprintf("k%05d", i), strings.Repeat("x", 50))
	}
	m.check(t)
	for i := 0; i < 3000; i++ {
		m.del(t, fmt.Sprintf("k%05d", i))
	}
	m.check(t)
	if _, ok := m.tree.Get([]byte("k00001")); ok {
		t.Fatal("key still present after delete")
	}
}

func TestBTreeScanStopsEarly(t *testing.T) {
	m := newMemTree()
	for i := 0; i < 500; i++ {
		m.set(t, fmt.Sprintf("k%04d", i), "v")
	}
	n := 0
	m.tree.Scan([]byte("k0100"), func(k, _ []byte) bool { n++; return n < 5 })
	if n != 5 {
		t.Fatalf("scan visited %d keys, want 5", n)
	}
}

func TestBTreeRejectsBadKeys(t *testing.T) {
	m := newMemTree()
	if err := m.tree.Insert(nil, []byte("v")); err == nil {
		t.Fatal("empty key must be rejected")
	}
	if _, err := m.tree.Delete(nil); err == nil {
		t.Fatal("deleting the sentinel (empty key) must be rejected")
	}
	if err := m.tree.Insert(bytes.Repeat([]byte("k"), BTREE_MAX_KEY_SIZE+1), nil); err == nil {
		t.Fatal("oversized key must be rejected")
	}
	if err := m.tree.Insert([]byte("k"), bytes.Repeat([]byte("v"), BTREE_MAX_VAL_SIZE+1)); err == nil {
		t.Fatal("oversized value must be rejected")
	}
}
