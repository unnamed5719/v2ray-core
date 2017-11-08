package crypto_test

import (
	"io"
	"testing"

	"v2ray.com/core/common/buf"
	. "v2ray.com/core/common/crypto"
	. "v2ray.com/ext/assert"
)

func TestChunkStreamIO(t *testing.T) {
	assert := With(t)

	cache := buf.NewLocal(8192)

	writer := NewChunkStreamWriter(PlainChunkSizeParser{}, cache)
	reader := NewChunkStreamReader(PlainChunkSizeParser{}, cache)

	b := buf.New()
	b.AppendBytes('a', 'b', 'c', 'd')
	assert(writer.Write(buf.NewMultiBufferValue(b)), IsNil)

	b = buf.New()
	b.AppendBytes('e', 'f', 'g')
	assert(writer.Write(buf.NewMultiBufferValue(b)), IsNil)

	assert(writer.Write(buf.NewMultiBuffer()), IsNil)

	assert(cache.Len(), Equals, 13)

	mb, err := reader.Read()
	assert(err, IsNil)
	assert(mb.Len(), Equals, 4)
	assert(mb[0].Bytes(), Equals, []byte("abcd"))

	mb, err = reader.Read()
	assert(err, IsNil)
	assert(mb.Len(), Equals, 3)
	assert(mb[0].Bytes(), Equals, []byte("efg"))

	_, err = reader.Read()
	assert(err, Equals, io.EOF)
}
