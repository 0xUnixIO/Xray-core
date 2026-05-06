package tcp

import (
	"github.com/0xUnixIO/Xray-core/common"
	"github.com/0xUnixIO/Xray-core/transport/internet"
)

func init() {
	common.Must(internet.RegisterProtocolConfigCreator(protocolName, func() interface{} {
		return new(Config)
	}))
}
