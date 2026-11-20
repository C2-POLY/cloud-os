package rpc

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/tobychui/smbsrv/internal/wire"
)

// PATCHES.md, 2: a bind whose max_recv_frag leaves no room for a stub (24 to
// 31 bytes upstream) made every reply loop forever, growing the pipe's output
// until the process ran out of memory. Such a bind now gets the smallest
// fragment every peer must take.
func TestPatchTinyRecvFragDoesNotLoop(t *testing.T) {
	for _, frag := range []uint16{0, 16, 24, 28, 31, 32, 100, minFrag} {
		p, _ := NewPipe("srvsvc", testEnv())
		body := bindBody(srvsvcUUID, 3)
		binary.LittleEndian.PutUint16(body[2:], frag) // max_recv_frag
		if err := p.Write(pdu(ptBind, flagFirstFrag|flagLastFrag, 1, body)); err != nil {
			t.Fatal(err)
		}
		p.Read(4096)

		// NetShareEnumAll, which has a reply of more than one fragment's room.
		n := serverNameStub()
		n.u32(1)
		n.u32(1)
		n.pointer()
		n.u32(0)
		n.null()
		n.u32(0)
		n.null()
		w := wire.NewWriter(64)
		w.U32(0)
		w.U16(0)
		w.U16(15)
		w.Bytes(n.B)
		done := make(chan error, 1)
		go func() { done <- p.Write(pdu(ptRequest, flagFirstFrag|flagLastFrag, 2, w.B)) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("max_recv_frag %d: %v", frag, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("max_recv_frag %d: the reply is still being split after 2 s", frag)
		}
		sawLast := false
		for p.Available() > 0 {
			head, _ := p.Read(headerSize)
			length := int(binary.LittleEndian.Uint16(head[8:]))
			if length <= headerSize+8 || length > minFrag && frag >= minFrag && length > int(frag) {
				t.Fatalf("max_recv_frag %d: fragment of %d bytes", frag, length)
			}
			sawLast = head[3]&flagLastFrag != 0
			p.Read(length - headerSize)
		}
		if !sawLast {
			t.Errorf("max_recv_frag %d: no last fragment", frag)
		}
	}
}
