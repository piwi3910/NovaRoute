package frr

import "fmt"

// YANG path templates for FRR northbound gRPC configuration.
// These follow the FRR YANG data model hierarchy.

// BGP YANG path constants.
const (
	// bgpBase is the root path for the default BGP instance.
	bgpBase = "/frr-routing:routing/control-plane-protocols/control-plane-protocol[type='frr-bgp:bgp'][name='default']/frr-bgp:bgp"

	// bgpGlobalAS is the path to the BGP autonomous system number.
	bgpGlobalAS = bgpBase + "/global/local-as"

	// bgpGlobalRouterID is the path to the BGP router ID.
	bgpGlobalRouterID = bgpBase + "/global/router-id"

	// bgpNeighborTemplate is the path template for a BGP neighbor keyed by remote address.
	// Use BGPNeighborPath(addr) to format.
	bgpNeighborTemplate = bgpBase + "/neighbors/neighbor[remote-address='%s']"

	// bgpNeighborRemoteAS is the sub-path for the neighbor's remote AS.
	bgpNeighborRemoteAS = "/remote-as"

	// bgpNeighborPeerType is the sub-path for the neighbor's peer type (internal/external).
	bgpNeighborPeerType = "/peer-type"

	// bgpNeighborTimersKeepalive is the sub-path for the neighbor keepalive timer.
	bgpNeighborTimersKeepalive = "/timers/keepalive"

	// bgpNeighborTimersHoldTime is the sub-path for the neighbor hold time.
	bgpNeighborTimersHoldTime = "/timers/hold-time"

	// bgpNeighborEnabled is the sub-path to enable/disable a neighbor.
	bgpNeighborEnabled = "/enabled"

	// bgpAFIIPv4Unicast is the AFI-SAFI name for IPv4 unicast.
	bgpAFIIPv4Unicast = "frr-routing:ipv4-unicast"

	// bgpAFIIPv6Unicast is the AFI-SAFI name for IPv6 unicast.
	bgpAFIIPv6Unicast = "frr-routing:ipv6-unicast"

	// bgpNeighborAFITemplate is the path template for a neighbor's AFI-SAFI activation.
	// Use BGPNeighborAFIPath(addr, afi) to format.
	bgpNeighborAFITemplate = bgpNeighborTemplate + "/afi-safis/afi-safi[afi-safi-name='%s']"

	// bgpNeighborAFIEnabled is the sub-path to enable an AFI-SAFI for a neighbor.
	bgpNeighborAFIEnabled = "/enabled"

	// bgpNetworkTemplate is the path template for an advertised BGP network.
	// Use BGPNetworkPath(prefix, afi) to format.
	bgpNetworkTemplate = bgpBase + "/global/afi-safis/afi-safi[afi-safi-name='%s']/network-config[prefix='%s']"
)

// BFD YANG path constants.
const (
	// bfdBase is the root path for BFD sessions.
	bfdBase = "/frr-bfdd:bfdd/bfd/sessions"

	// bfdSingleHopTemplate is the path template for a single-hop BFD session.
	// Use BFDPeerPath(addr) to format.
	bfdSingleHopTemplate = bfdBase + "/single-hop[dest-addr='%s']"

	// bfdMinRxInterval is the sub-path for minimum receive interval.
	bfdMinRxInterval = "/required-receive-interval"

	// bfdMinTxInterval is the sub-path for minimum transmit interval.
	bfdMinTxInterval = "/desired-transmission-interval"

	// bfdDetectMultiplier is the sub-path for the detection multiplier.
	bfdDetectMultiplier = "/detection-multiplier"

	// bfdInterface is the sub-path for the interface.
	bfdInterface = "/interface"
)

// OSPF YANG path constants.
const (
	// ospfBase is the root path for the default OSPF instance.
	ospfBase = "/frr-routing:routing/control-plane-protocols/control-plane-protocol[type='frr-ospfd:ospf'][name='default']/frr-ospfd:ospf"

	// ospfAreaTemplate is the path template for an OSPF area.
	// Use OSPFAreaPath(areaID) to format.
	ospfAreaTemplate = ospfBase + "/areas/area[area-id='%s']"

	// ospfInterfaceTemplate is the path template for an interface within an OSPF area.
	// Use OSPFInterfacePath(ifaceName, areaID) to format.
	ospfInterfaceTemplate = ospfAreaTemplate + "/interfaces/interface[name='%s']"

	// ospfInterfacePassive is the sub-path for passive mode.
	ospfInterfacePassive = "/passive"

	// ospfInterfaceCost is the sub-path for the interface cost.
	ospfInterfaceCost = "/cost"

	// ospfInterfaceHelloInterval is the sub-path for the hello interval.
	ospfInterfaceHelloInterval = "/hello-interval"

	// ospfInterfaceDeadInterval is the sub-path for the dead interval.
	ospfInterfaceDeadInterval = "/dead-interval"
)

// BGPNeighborPath returns the YANG path for a BGP neighbor by IP address.
func BGPNeighborPath(addr string) string {
	return fmt.Sprintf(bgpNeighborTemplate, addr)
}

// BGPNeighborAFIPath returns the YANG path for a neighbor's AFI-SAFI.
func BGPNeighborAFIPath(addr, afi string) string {
	return fmt.Sprintf(bgpNeighborAFITemplate, addr, afi)
}

// BGPNetworkPath returns the YANG path for a BGP network advertisement.
func BGPNetworkPath(prefix, afi string) string {
	return fmt.Sprintf(bgpNetworkTemplate, afi, prefix)
}

// BFDPeerPath returns the YANG path for a single-hop BFD peer.
func BFDPeerPath(addr string) string {
	return fmt.Sprintf(bfdSingleHopTemplate, addr)
}

// OSPFAreaPath returns the YANG path for an OSPF area.
func OSPFAreaPath(areaID string) string {
	return fmt.Sprintf(ospfAreaTemplate, areaID)
}

// OSPFInterfacePath returns the YANG path for an interface in an OSPF area.
func OSPFInterfacePath(ifaceName, areaID string) string {
	return fmt.Sprintf(ospfInterfaceTemplate, areaID, ifaceName)
}

// resolveAFI maps user-friendly AFI names to FRR YANG AFI-SAFI identifiers.
func resolveAFI(afi string) string {
	switch afi {
	case "ipv4-unicast", "ipv4":
		return bgpAFIIPv4Unicast
	case "ipv6-unicast", "ipv6":
		return bgpAFIIPv6Unicast
	default:
		return afi
	}
}
