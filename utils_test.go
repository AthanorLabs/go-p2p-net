package net

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateAndSaveKey(t *testing.T) {
	tempDir := t.TempDir()
	_, err := generateKey(1234, tempDir)
	require.NoError(t, err)

	_, err = generateKey(1234, tempDir)
	require.NoError(t, err)
}

func Test_stringsToAddrInfos(t *testing.T) {
	bootnodes := []string{
		"/ip4/192.168.0.101/udp/9934/quic-v1/p2p/12D3KooWC547RfLcveQi1vBxACjnT6Uv15V11ortDTuxRWuhubGv",
		"/ip4/192.168.0.101/tcp/9934/p2p/12D3KooWC547RfLcveQi1vBxACjnT6Uv15V11ortDTuxRWuhubGv",
	}
	addrInfos, err := stringsToAddrInfos(bootnodes)
	require.NoError(t, err)
	require.Len(t, addrInfos, 1) // both were combined into one AddrInfo
	require.Equal(t, "12D3KooWC547RfLcveQi1vBxACjnT6Uv15V11ortDTuxRWuhubGv",
		addrInfos[0].ID.String())
	require.Len(t, addrInfos[0].Addrs, 2)
	require.Equal(t, "/ip4/192.168.0.101/udp/9934/quic-v1", addrInfos[0].Addrs[0].String())
	require.Equal(t, "/ip4/192.168.0.101/tcp/9934", addrInfos[0].Addrs[1].String())
}

func Test_readStreamMessage(t *testing.T) {
	msgBytes := []byte("testmessage")
	var lenBytes [4]byte
	binary.LittleEndian.PutUint32(lenBytes[:], uint32(len(msgBytes)))
	streamData := append(lenBytes[:], msgBytes...)
	stream := bytes.NewReader(streamData)
	readMsg, err := ReadStreamMessage(stream, testMaxMessageSize)
	require.NoError(t, err)
	require.Equal(t, msgBytes, readMsg)
}

func Test_readStreamMessage_EOF(t *testing.T) {
	// If the stream is closed before we read a length value, no message was truncated and
	// the returned error is io.EOF
	stream := bytes.NewReader(nil)
	_, err := ReadStreamMessage(stream, testMaxMessageSize)
	require.ErrorIs(t, err, io.EOF) // connection closed before we read any length

	// If the message was truncated either in the length or body, the error is io.ErrUnexpectedEOF
	serializedData := []byte{0x1} // truncated length
	stream = bytes.NewReader(serializedData)
	_, err = ReadStreamMessage(stream, testMaxMessageSize)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF) // connection after we read at least one byte

	serializedData = []byte{0x1, 0, 0, 0} // truncated encoded message
	stream = bytes.NewReader(serializedData)
	_, err = ReadStreamMessage(stream, testMaxMessageSize)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF) // connection after we read at least one byte
}

func Test_readStreamMessage_TooLarge(t *testing.T) {
	buf := make([]byte, 4+testMaxMessageSize+1)
	binary.LittleEndian.PutUint32(buf, testMaxMessageSize+1)
	_, err := ReadStreamMessage(bytes.NewReader(buf), testMaxMessageSize)
	require.ErrorContains(t, err, "too large")
}

func Test_readStreamMessage_NilStream(t *testing.T) {
	// Can our code actually trigger this error?
	_, err := ReadStreamMessage(nil, testMaxMessageSize)
	require.ErrorIs(t, err, errNilStream)
}

func Test_writeStreamMessage(t *testing.T) {
	msg := []byte("testmessage")
	stream := &bytes.Buffer{}
	err := writeStreamBytes(stream, msg)
	require.NoError(t, err)

	serializedData := stream.Bytes()
	require.Greater(t, len(serializedData), 4)

	lenMsg := binary.LittleEndian.Uint32(serializedData)
	msgBytes := serializedData[4:]
	require.Equal(t, int(lenMsg), len(msgBytes))
	require.Equal(t, msg, msgBytes)
}
