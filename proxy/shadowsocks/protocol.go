package shadowsocks

import (
	"bytes"
	"crypto/rand"
	"io"

	"v2ray.com/core/common"
	"v2ray.com/core/common/bitmask"
	"v2ray.com/core/common/buf"
	"v2ray.com/core/common/net"
	"v2ray.com/core/common/protocol"
	"v2ray.com/core/proxy/socks"
)

const (
	Version                               = 1
	RequestOptionOneTimeAuth bitmask.Byte = 0x01

	AddrTypeIPv4   = 1
	AddrTypeIPv6   = 4
	AddrTypeDomain = 3
)

func ReadTCPSession(user *protocol.User, reader io.Reader) (*protocol.RequestHeader, buf.Reader, error) {
	rawAccount, err := user.GetTypedAccount()
	if err != nil {
		return nil, nil, newError("failed to parse account").Base(err).AtError()
	}
	account := rawAccount.(*MemoryAccount)

	buffer := buf.NewLocal(512)
	defer buffer.Release()

	ivLen := account.Cipher.IVSize()
	var iv []byte
	if ivLen > 0 {
		if err := buffer.AppendSupplier(buf.ReadFullFrom(reader, ivLen)); err != nil {
			return nil, nil, newError("failed to read IV").Base(err)
		}

		iv = append([]byte(nil), buffer.BytesTo(ivLen)...)
	}

	r, err := account.Cipher.NewDecryptionReader(account.Key, iv, reader)
	if err != nil {
		return nil, nil, newError("failed to initialize decoding stream").Base(err).AtError()
	}
	br := buf.NewBufferedReader(r)
	reader = nil

	authenticator := NewAuthenticator(HeaderKeyGenerator(account.Key, iv))
	request := &protocol.RequestHeader{
		Version: Version,
		User:    user,
		Command: protocol.RequestCommandTCP,
	}

	if err := buffer.Reset(buf.ReadFullFrom(br, 1)); err != nil {
		return nil, nil, newError("failed to read address type").Base(err)
	}

	if !account.Cipher.IsAEAD() {
		if (buffer.Byte(0) & 0x10) == 0x10 {
			request.Option.Set(RequestOptionOneTimeAuth)
		}

		if request.Option.Has(RequestOptionOneTimeAuth) && account.OneTimeAuth == Account_Disabled {
			return nil, nil, newError("rejecting connection with OTA enabled, while server disables OTA")
		}

		if !request.Option.Has(RequestOptionOneTimeAuth) && account.OneTimeAuth == Account_Enabled {
			return nil, nil, newError("rejecting connection with OTA disabled, while server enables OTA")
		}
	}

	addrType := (buffer.Byte(0) & 0x0F)
	switch addrType {
	case AddrTypeIPv4:
		if err := buffer.AppendSupplier(buf.ReadFullFrom(br, 4)); err != nil {
			return nil, nil, newError("failed to read IPv4 address").Base(err)
		}
		request.Address = net.IPAddress(buffer.BytesFrom(-4))
	case AddrTypeIPv6:
		if err := buffer.AppendSupplier(buf.ReadFullFrom(br, 16)); err != nil {
			return nil, nil, newError("failed to read IPv6 address").Base(err)
		}
		request.Address = net.IPAddress(buffer.BytesFrom(-16))
	case AddrTypeDomain:
		if err := buffer.AppendSupplier(buf.ReadFullFrom(br, 1)); err != nil {
			return nil, nil, newError("failed to read domain lenth.").Base(err)
		}
		domainLength := int(buffer.BytesFrom(-1)[0])
		err = buffer.AppendSupplier(buf.ReadFullFrom(br, domainLength))
		if err != nil {
			return nil, nil, newError("failed to read domain").Base(err)
		}
		request.Address = net.DomainAddress(string(buffer.BytesFrom(-domainLength)))
	default:
		// Check address validity after OTA verification.
	}

	err = buffer.AppendSupplier(buf.ReadFullFrom(br, 2))
	if err != nil {
		return nil, nil, newError("failed to read port").Base(err)
	}
	request.Port = net.PortFromBytes(buffer.BytesFrom(-2))

	if request.Option.Has(RequestOptionOneTimeAuth) {
		actualAuth := make([]byte, AuthSize)
		authenticator.Authenticate(buffer.Bytes())(actualAuth)

		err := buffer.AppendSupplier(buf.ReadFullFrom(br, AuthSize))
		if err != nil {
			return nil, nil, newError("Failed to read OTA").Base(err)
		}

		if !bytes.Equal(actualAuth, buffer.BytesFrom(-AuthSize)) {
			return nil, nil, newError("invalid OTA")
		}
	}

	if request.Address == nil {
		return nil, nil, newError("invalid remote address.")
	}

	br.SetBuffered(false)

	var chunkReader buf.Reader
	if request.Option.Has(RequestOptionOneTimeAuth) {
		chunkReader = NewChunkReader(br, NewAuthenticator(ChunkKeyGenerator(iv)))
	} else {
		chunkReader = buf.NewReader(br)
	}

	return request, chunkReader, nil
}

func WriteTCPRequest(request *protocol.RequestHeader, writer io.Writer) (buf.Writer, error) {
	user := request.User
	rawAccount, err := user.GetTypedAccount()
	if err != nil {
		return nil, newError("failed to parse account").Base(err).AtError()
	}
	account := rawAccount.(*MemoryAccount)

	if account.Cipher.IsAEAD() {
		request.Option.Clear(RequestOptionOneTimeAuth)
	}

	var iv []byte
	if account.Cipher.IVSize() > 0 {
		iv = make([]byte, account.Cipher.IVSize())
		common.Must2(rand.Read(iv))
		_, err = writer.Write(iv)
		if err != nil {
			return nil, newError("failed to write IV")
		}
	}

	w, err := account.Cipher.NewEncryptionWriter(account.Key, iv, writer)
	if err != nil {
		return nil, newError("failed to create encoding stream").Base(err).AtError()
	}

	header := buf.NewLocal(512)

	if err := socks.AppendAddress(header, request.Address, request.Port); err != nil {
		return nil, newError("failed to write address").Base(err)
	}

	if request.Option.Has(RequestOptionOneTimeAuth) {
		header.SetByte(0, header.Byte(0)|0x10)

		authenticator := NewAuthenticator(HeaderKeyGenerator(account.Key, iv))
		common.Must(header.AppendSupplier(authenticator.Authenticate(header.Bytes())))
	}

	if err := w.WriteMultiBuffer(buf.NewMultiBufferValue(header)); err != nil {
		return nil, newError("failed to write header").Base(err)
	}

	var chunkWriter buf.Writer
	if request.Option.Has(RequestOptionOneTimeAuth) {
		chunkWriter = NewChunkWriter(w.(io.Writer), NewAuthenticator(ChunkKeyGenerator(iv)))
	} else {
		chunkWriter = w
	}

	return chunkWriter, nil
}

func ReadTCPResponse(user *protocol.User, reader io.Reader) (buf.Reader, error) {
	rawAccount, err := user.GetTypedAccount()
	if err != nil {
		return nil, newError("failed to parse account").Base(err).AtError()
	}
	account := rawAccount.(*MemoryAccount)

	var iv []byte
	if account.Cipher.IVSize() > 0 {
		iv = make([]byte, account.Cipher.IVSize())
		_, err = io.ReadFull(reader, iv)
		if err != nil {
			return nil, newError("failed to read IV").Base(err)
		}
	}

	return account.Cipher.NewDecryptionReader(account.Key, iv, reader)
}

func WriteTCPResponse(request *protocol.RequestHeader, writer io.Writer) (buf.Writer, error) {
	user := request.User
	rawAccount, err := user.GetTypedAccount()
	if err != nil {
		return nil, newError("failed to parse account.").Base(err).AtError()
	}
	account := rawAccount.(*MemoryAccount)

	var iv []byte
	if account.Cipher.IVSize() > 0 {
		iv = make([]byte, account.Cipher.IVSize())
		common.Must2(rand.Read(iv))
		_, err = writer.Write(iv)
		if err != nil {
			return nil, newError("failed to write IV.").Base(err)
		}
	}

	return account.Cipher.NewEncryptionWriter(account.Key, iv, writer)
}

func EncodeUDPPacket(request *protocol.RequestHeader, payload []byte) (*buf.Buffer, error) {
	user := request.User
	rawAccount, err := user.GetTypedAccount()
	if err != nil {
		return nil, newError("failed to parse account.").Base(err).AtError()
	}
	account := rawAccount.(*MemoryAccount)

	buffer := buf.New()
	ivLen := account.Cipher.IVSize()
	if ivLen > 0 {
		common.Must(buffer.Reset(buf.ReadFullFrom(rand.Reader, ivLen)))
	}
	iv := buffer.Bytes()

	if err := socks.AppendAddress(buffer, request.Address, request.Port); err != nil {
		return nil, newError("failed to write address").Base(err)
	}

	buffer.Append(payload)

	if !account.Cipher.IsAEAD() && request.Option.Has(RequestOptionOneTimeAuth) {
		authenticator := NewAuthenticator(HeaderKeyGenerator(account.Key, iv))
		buffer.SetByte(ivLen, buffer.Byte(ivLen)|0x10)

		common.Must(buffer.AppendSupplier(authenticator.Authenticate(buffer.BytesFrom(ivLen))))
	}
	if err := account.Cipher.EncodePacket(account.Key, buffer); err != nil {
		return nil, newError("failed to encrypt UDP payload").Base(err)
	}

	return buffer, nil
}

func DecodeUDPPacket(user *protocol.User, payload *buf.Buffer) (*protocol.RequestHeader, *buf.Buffer, error) {
	rawAccount, err := user.GetTypedAccount()
	if err != nil {
		return nil, nil, newError("failed to parse account").Base(err).AtError()
	}
	account := rawAccount.(*MemoryAccount)

	var iv []byte
	if !account.Cipher.IsAEAD() && account.Cipher.IVSize() > 0 {
		// Keep track of IV as it gets removed from payload in DecodePacket.
		iv = make([]byte, account.Cipher.IVSize())
		copy(iv, payload.BytesTo(account.Cipher.IVSize()))
	}

	if err := account.Cipher.DecodePacket(account.Key, payload); err != nil {
		return nil, nil, newError("failed to decrypt UDP payload").Base(err)
	}

	request := &protocol.RequestHeader{
		Version: Version,
		User:    user,
		Command: protocol.RequestCommandUDP,
	}

	if !account.Cipher.IsAEAD() {
		if (payload.Byte(0) & 0x10) == 0x10 {
			request.Option |= RequestOptionOneTimeAuth
		}

		if request.Option.Has(RequestOptionOneTimeAuth) && account.OneTimeAuth == Account_Disabled {
			return nil, nil, newError("rejecting packet with OTA enabled, while server disables OTA").AtWarning()
		}

		if !request.Option.Has(RequestOptionOneTimeAuth) && account.OneTimeAuth == Account_Enabled {
			return nil, nil, newError("rejecting packet with OTA disabled, while server enables OTA").AtWarning()
		}

		if request.Option.Has(RequestOptionOneTimeAuth) {
			payloadLen := payload.Len() - AuthSize
			authBytes := payload.BytesFrom(payloadLen)

			authenticator := NewAuthenticator(HeaderKeyGenerator(account.Key, iv))
			actualAuth := make([]byte, AuthSize)
			authenticator.Authenticate(payload.BytesTo(payloadLen))(actualAuth)
			if !bytes.Equal(actualAuth, authBytes) {
				return nil, nil, newError("invalid OTA")
			}

			payload.Slice(0, payloadLen)
		}
	}

	addrType := (payload.Byte(0) & 0x0F)
	payload.SliceFrom(1)
	switch addrType {
	case AddrTypeIPv4:
		request.Address = net.IPAddress(payload.BytesTo(4))
		payload.SliceFrom(4)
	case AddrTypeIPv6:
		request.Address = net.IPAddress(payload.BytesTo(16))
		payload.SliceFrom(16)
	case AddrTypeDomain:
		domainLength := int(payload.Byte(0))
		request.Address = net.DomainAddress(string(payload.BytesRange(1, 1+domainLength)))
		payload.SliceFrom(1 + domainLength)
	default:
		return nil, nil, newError("unknown address type: ", addrType).AtError()
	}

	request.Port = net.PortFromBytes(payload.BytesTo(2))
	payload.SliceFrom(2)

	return request, payload, nil
}

type UDPReader struct {
	Reader io.Reader
	User   *protocol.User
}

func (v *UDPReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	buffer := buf.New()
	err := buffer.AppendSupplier(buf.ReadFrom(v.Reader))
	if err != nil {
		buffer.Release()
		return nil, err
	}
	_, payload, err := DecodeUDPPacket(v.User, buffer)
	if err != nil {
		buffer.Release()
		return nil, err
	}
	return buf.NewMultiBufferValue(payload), nil
}

type UDPWriter struct {
	Writer  io.Writer
	Request *protocol.RequestHeader
}

// Write implements io.Writer.
func (w *UDPWriter) Write(payload []byte) (int, error) {
	packet, err := EncodeUDPPacket(w.Request, payload)
	if err != nil {
		return 0, err
	}
	_, err = w.Writer.Write(packet.Bytes())
	packet.Release()
	return len(payload), err
}
