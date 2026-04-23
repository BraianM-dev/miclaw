package network

import "net"

// Location describes an MPLS network site.
type Location struct {
	Name     string `json:"name"`
	CIDR     string `json:"cidr"`
	Gateway  string `json:"gateway"`
	Region   string `json:"region"`
}

var table = []Location{
	{Name: "Casa Central",       CIDR: "10.10.0.0/16",  Gateway: "10.10.0.1",  Region: "AMBA"},
	{Name: "Sucursal Norte",     CIDR: "10.20.0.0/16",  Gateway: "10.20.0.1",  Region: "GBA Norte"},
	{Name: "Sucursal Sur",       CIDR: "10.30.0.0/16",  Gateway: "10.30.0.1",  Region: "GBA Sur"},
	{Name: "Sucursal Oeste",     CIDR: "10.40.0.0/16",  Gateway: "10.40.0.1",  Region: "GBA Oeste"},
	{Name: "Data Center",        CIDR: "10.50.0.0/16",  Gateway: "10.50.0.1",  Region: "Colocation"},
	{Name: "Rosario",            CIDR: "10.60.0.0/16",  Gateway: "10.60.0.1",  Region: "Interior"},
	{Name: "Córdoba",            CIDR: "10.70.0.0/16",  Gateway: "10.70.0.1",  Region: "Interior"},
	{Name: "Mendoza",            CIDR: "10.80.0.0/16",  Gateway: "10.80.0.1",  Region: "Interior"},
	{Name: "Tucumán",            CIDR: "10.90.0.0/16",  Gateway: "10.90.0.1",  Region: "Interior"},
	{Name: "VPN Remota",         CIDR: "172.16.0.0/12", Gateway: "172.16.0.1", Region: "Remoto"},
	{Name: "Red Local",          CIDR: "192.168.0.0/16", Gateway: "192.168.1.1", Region: "LAN"},
}

// All returns the full routing table.
func All() []Location { return table }

// Resolve returns the location name and gateway for the given IP.
func Resolve(ip string) (name, gateway string) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "Desconocida", ""
	}
	for _, loc := range table {
		_, cidr, err := net.ParseCIDR(loc.CIDR)
		if err != nil {
			continue
		}
		if cidr.Contains(parsed) {
			return loc.Name, loc.Gateway
		}
	}
	return "Externa", ""
}
