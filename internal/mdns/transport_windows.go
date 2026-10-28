package mdns

import (
	"encoding/binary"
	"errors"
	"net"
	"unsafe"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/windows"
)

const controlHeaderSize = int(unsafe.Sizeof(windows.WSACMSGHDR{}))
const controlWordSize = int(unsafe.Sizeof(uintptr(0)))

func setPacketInfo(c *net.UDPConn, protocol, option int, enabled bool) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	value := 0
	if enabled {
		value = 1
	}
	var optionErr error
	if err = raw.Control(func(fd uintptr) { optionErr = windows.SetsockoptInt(windows.Handle(fd), protocol, option, value) }); err != nil {
		return err
	}
	return optionErr
}
func (c *packetConn4) SetControlMessage(flags ipv4.ControlFlags, on bool) error {
	if flags != ipv4.FlagInterface {
		return errors.New("mDNS: unsupported packet control flag")
	}
	return setPacketInfo(c.raw, windows.IPPROTO_IP, windows.IP_PKTINFO, on)
}
func (c *packetConn6) SetControlMessage(flags ipv6.ControlFlags, on bool) error {
	if flags != ipv6.FlagInterface {
		return errors.New("mDNS: unsupported packet control flag")
	}
	return setPacketInfo(c.raw, windows.IPPROTO_IPV6, windows.IPV6_PKTINFO, on)
}

func alignControl(n int) int { return (n + controlWordSize - 1) &^ (controlWordSize - 1) }
func packetInfoIndex(control []byte, protocol, option int, size, indexOffset int) (int, error) {
	index := 0
	for len(control) >= controlHeaderSize {
		length := uint64(binary.LittleEndian.Uint32(control))
		if controlWordSize == 8 {
			length = binary.LittleEndian.Uint64(control)
		}
		if length < uint64(controlHeaderSize) || length > uint64(len(control)) {
			return 0, errors.New("mDNS: malformed packet control length")
		}
		level := int(binary.LittleEndian.Uint32(control[controlWordSize:]))
		kind := int(binary.LittleEndian.Uint32(control[controlWordSize+4:]))
		if level == protocol && kind == option {
			if int(length)-controlHeaderSize < size {
				return 0, errors.New("mDNS: truncated packet interface metadata")
			}
			next := int(binary.LittleEndian.Uint32(control[controlHeaderSize+indexOffset:]))
			if next <= 0 || (index != 0 && index != next) {
				return 0, errors.New("mDNS: ambiguous packet interface metadata")
			}
			index = next
		}
		next := alignControl(int(length))
		if next >= len(control) {
			break
		}
		control = control[next:]
	}
	return index, nil
}
func packetInfoControl(protocol, option, size, indexOffset, index int) []byte {
	length := controlHeaderSize + size
	control := make([]byte, alignControl(length))
	if controlWordSize == 8 {
		binary.LittleEndian.PutUint64(control, uint64(length))
	} else {
		binary.LittleEndian.PutUint32(control, uint32(length))
	}
	binary.LittleEndian.PutUint32(control[controlWordSize:], uint32(protocol))
	binary.LittleEndian.PutUint32(control[controlWordSize+4:], uint32(option))
	binary.LittleEndian.PutUint32(control[controlHeaderSize+indexOffset:], uint32(index))
	return control
}

func (c *packetConn4) ReadFrom(data []byte) (int, *ipv4.ControlMessage, net.Addr, error) {
	var control [128]byte
	n, oob, flags, from, err := c.raw.ReadMsgUDP(data, control[:])
	if err != nil {
		return n, nil, from, err
	}
	if flags&windows.MSG_CTRUNC != 0 {
		return 0, nil, from, errors.New("mDNS: truncated packet control buffer")
	}
	index, err := packetInfoIndex(control[:oob], windows.IPPROTO_IP, windows.IP_PKTINFO, 8, 4)
	return n, &ipv4.ControlMessage{IfIndex: index}, from, err
}
func (c *packetConn6) ReadFrom(data []byte) (int, *ipv6.ControlMessage, net.Addr, error) {
	var control [128]byte
	n, oob, flags, from, err := c.raw.ReadMsgUDP(data, control[:])
	if err != nil {
		return n, nil, from, err
	}
	if flags&windows.MSG_CTRUNC != 0 {
		return 0, nil, from, errors.New("mDNS: truncated packet control buffer")
	}
	index, err := packetInfoIndex(control[:oob], windows.IPPROTO_IPV6, windows.IPV6_PKTINFO, 20, 16)
	return n, &ipv6.ControlMessage{IfIndex: index}, from, err
}
func (c *packetConn4) WriteTo(data []byte, cm *ipv4.ControlMessage, to net.Addr) (int, error) {
	address, ok := to.(*net.UDPAddr)
	if !ok {
		return 0, errors.New("mDNS: expected UDP address")
	}
	if cm == nil || cm.IfIndex == 0 {
		return c.raw.WriteToUDP(data, address)
	}
	n, _, err := c.raw.WriteMsgUDP(data, packetInfoControl(windows.IPPROTO_IP, windows.IP_PKTINFO, 8, 4, cm.IfIndex), address)
	return n, err
}
func (c *packetConn6) WriteTo(data []byte, cm *ipv6.ControlMessage, to net.Addr) (int, error) {
	address, ok := to.(*net.UDPAddr)
	if !ok {
		return 0, errors.New("mDNS: expected UDP address")
	}
	if cm == nil || cm.IfIndex == 0 {
		return c.raw.WriteToUDP(data, address)
	}
	n, _, err := c.raw.WriteMsgUDP(data, packetInfoControl(windows.IPPROTO_IPV6, windows.IPV6_PKTINFO, 20, 16, cm.IfIndex), address)
	return n, err
}
