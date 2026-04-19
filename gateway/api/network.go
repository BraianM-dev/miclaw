package api

// network.go — Tabla de ruteo MPLS interna para AFE.
// Resuelve la ubicación geográfica de un equipo por su IP de red.
// No ejecuta comandos de sistema: pura lógica Go con net.ParseCIDR.

import (
	"net"
)

// networkRoute almacena la ubicación y gateway para una subred MPLS.
type networkRoute struct {
	Location string
	Gateway  string
}

// afeRoutes es la tabla de ruteo interno del MPLS de AFE.
// Clave: CIDR. Valor: ubicación + gateway.
var afeRoutes = map[string]networkRoute{
	"192.168.1.0/24":  {Location: "Baalbek Centro Montevideo", Gateway: "192.168.1.11"},
	"192.168.2.0/24":  {Location: "Peñarol Talleres", Gateway: "192.168.2.5"},
	"192.168.3.0/24":  {Location: "Jefatura Trafico Sayago", Gateway: "192.168.3.10"},
	"192.168.4.0/24":  {Location: "Remesa Paysandu", Gateway: "192.168.4.10"},
	"192.168.5.0/24":  {Location: "Remesa Paso de los Toros", Gateway: "192.168.5.10"},
	"192.168.6.0/24":  {Location: "Estacion Toledo", Gateway: "192.168.6.10"},
	"192.168.10.0/24": {Location: "Estacion Peñarol", Gateway: "192.168.10.10"},
	"192.168.15.0/24": {Location: "Regional Via y Obras Toledo", Gateway: "192.168.15.10"},
	"192.168.20.0/24": {Location: "Regional Via y Obras Sayago", Gateway: "192.168.20.10"},
	"192.168.21.0/24": {Location: "Regional Paso de los Toros", Gateway: "192.168.21.10"},
	"192.168.22.0/24": {Location: "Regional Paysandu", Gateway: "192.168.22.10"},
}

// ResolveLocation devuelve el nombre de ubicación para la IP dada.
// Devuelve cadena vacía si la IP no coincide con ninguna subred conocida.
func ResolveLocation(ip string) string {
	r, _ := resolveRoute(ip)
	return r.Location
}

// ResolveGateway devuelve el gateway MPLS para la IP dada.
func ResolveGateway(ip string) string {
	r, _ := resolveRoute(ip)
	return r.Gateway
}

// ResolveRoute devuelve ubicación + gateway para la IP dada.
func ResolveRoute(ip string) (location, gateway string) {
	r, ok := resolveRoute(ip)
	if !ok {
		return "", ""
	}
	return r.Location, r.Gateway
}

func resolveRoute(ip string) (networkRoute, bool) {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return networkRoute{}, false
	}
	for cidr, route := range afeRoutes {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(parsed) {
			return route, true
		}
	}
	return networkRoute{}, false
}

// AllLocations devuelve el mapa completo de ubicaciones (para el frontend).
func AllLocations() map[string]networkRoute {
	out := make(map[string]networkRoute, len(afeRoutes))
	for k, v := range afeRoutes {
		out[k] = v
	}
	return out
}
