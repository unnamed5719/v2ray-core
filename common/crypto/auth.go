package crypto

import (
	"crypto/cipher"
	"io"

	"v2ray.com/core/common"
	"v2ray.com/core/common/buf"
	"v2ray.com/core/common/protocol"
)

type BytesGenerator interface {
	Next() []byte
}

type NoOpBytesGenerator struct {
	buffer [1]byte
}

func (v NoOpBytesGenerator) Next() []byte {
	return v.buffer[:0]
}

type StaticBytesGenerator struct {
	Content []byte
}

func (v StaticBytesGenerator) Next() []byte {
	return v.Content
}

type IncreasingAEADNonceGenerator struct {
	nonce []byte
}

func NewIncreasingAEADNonceGenerator() *IncreasingAEADNonceGenerator {
	return &IncreasingAEADNonceGenerator{
		nonce: []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
	}
}

func (g *IncreasingAEADNonceGenerator) Next() []byte {
	for i := range g.nonce {
		g.nonce[i]++
		if g.nonce[i] != 0 {
			break
		}
	}
	return g.nonce
}

type Authenticator interface {
	NonceSize() int
	Overhead() int
	Open(dst, cipherText []byte) ([]byte, error)
	Seal(dst, plainText []byte) ([]byte, error)
}

type AEADAuthenticator struct {
	cipher.AEAD
	NonceGenerator          BytesGenerator
	AdditionalDataGenerator BytesGenerator
}

func (v *AEADAuthenticator) Open(dst, cipherText []byte) ([]byte, error) {
	iv := v.NonceGenerator.Next()
	if len(iv) != v.AEAD.NonceSize() {
		return nil, newError("invalid AEAD nonce size: ", len(iv))
	}

	var additionalData []byte
	if v.AdditionalDataGenerator != nil {
		additionalData = v.AdditionalDataGenerator.Next()
	}
	return v.AEAD.Open(dst, iv, cipherText, additionalData)
}

func (v *AEADAuthenticator) Seal(dst, plainText []byte) ([]byte, error) {
	iv := v.NonceGenerator.Next()
	if len(iv) != v.AEAD.NonceSize() {
		return nil, newError("invalid AEAD nonce size: ", len(iv))
	}

	var additionalData []byte
	if v.AdditionalDataGenerator != nil {
		additionalData = v.AdditionalDataGenerator.Next()
	}
	return v.AEAD.Seal(dst, iv, plainText, additionalData), nil
}

type AuthenticationReader struct {
	auth         Authenticator
	reader       *buf.BufferedReader
	sizeParser   ChunkSizeDecoder
	transferType protocol.TransferType
}

func NewAuthenticationReader(auth Authenticator, sizeParser ChunkSizeDecoder, reader io.Reader, transferType protocol.TransferType) *AuthenticationReader {
	return &AuthenticationReader{
		auth:         auth,
		reader:       buf.NewBufferedReader(buf.NewReader(reader)),
		sizeParser:   sizeParser,
		transferType: transferType,
	}
}

func (r *AuthenticationReader) readSize() (int, error) {
	sizeBytes := make([]byte, r.sizeParser.SizeBytes())
	_, err := io.ReadFull(r.reader, sizeBytes)
	if err != nil {
		return 0, err
	}
	size, err := r.sizeParser.Decode(sizeBytes)
	return int(size), err
}

func (r *AuthenticationReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	size, err := r.readSize()
	if err != nil {
		return nil, err
	}

	if size == r.auth.Overhead() {
		return nil, io.EOF
	}

	var b *buf.Buffer
	if size <= buf.Size {
		b = buf.New()
	} else {
		b = buf.NewLocal(size)
	}
	if err := b.Reset(buf.ReadFullFrom(r.reader, size)); err != nil {
		b.Release()
		return nil, err
	}

	rb, err := r.auth.Open(b.BytesTo(0), b.BytesTo(size))
	if err != nil {
		b.Release()
		return nil, err
	}
	b.Slice(0, len(rb))
	return buf.NewMultiBufferValue(b), nil
}

type AuthenticationWriter struct {
	auth         Authenticator
	writer       buf.Writer
	sizeParser   ChunkSizeEncoder
	transferType protocol.TransferType
}

func NewAuthenticationWriter(auth Authenticator, sizeParser ChunkSizeEncoder, writer io.Writer, transferType protocol.TransferType) *AuthenticationWriter {
	return &AuthenticationWriter{
		auth:         auth,
		writer:       buf.NewWriter(writer),
		sizeParser:   sizeParser,
		transferType: transferType,
	}
}

func (w *AuthenticationWriter) seal(b *buf.Buffer) (*buf.Buffer, error) {
	encryptedSize := b.Len() + w.auth.Overhead()

	eb := buf.New()
	common.Must(eb.Reset(func(bb []byte) (int, error) {
		w.sizeParser.Encode(uint16(encryptedSize), bb[:0])
		return w.sizeParser.SizeBytes(), nil
	}))
	if err := eb.AppendSupplier(func(bb []byte) (int, error) {
		_, err := w.auth.Seal(bb[:0], b.Bytes())
		return encryptedSize, err
	}); err != nil {
		eb.Release()
		return nil, err
	}

	return eb, nil
}

func (w *AuthenticationWriter) writeStream(mb buf.MultiBuffer) error {
	defer mb.Release()

	payloadSize := buf.Size - w.auth.Overhead() - w.sizeParser.SizeBytes()
	mb2Write := buf.NewMultiBufferCap(len(mb) + 10)

	for {
		b := buf.New()
		common.Must(b.Reset(func(bb []byte) (int, error) {
			return mb.Read(bb[:payloadSize])
		}))
		eb, err := w.seal(b)
		b.Release()

		if err != nil {
			mb2Write.Release()
			return err
		}
		mb2Write.Append(eb)
		if mb.IsEmpty() {
			break
		}
	}

	return w.writer.WriteMultiBuffer(mb2Write)
}

func (w *AuthenticationWriter) writePacket(mb buf.MultiBuffer) error {
	defer mb.Release()

	mb2Write := buf.NewMultiBufferCap(len(mb) * 2)

	for {
		b := mb.SplitFirst()
		if b == nil {
			b = buf.New()
		}
		eb, err := w.seal(b)
		b.Release()
		if err != nil {
			mb2Write.Release()
			return err
		}
		mb2Write.Append(eb)
		if mb.IsEmpty() {
			break
		}
	}

	return w.writer.WriteMultiBuffer(mb2Write)
}

func (w *AuthenticationWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if w.transferType == protocol.TransferTypeStream {
		return w.writeStream(mb)
	}

	return w.writePacket(mb)
}
