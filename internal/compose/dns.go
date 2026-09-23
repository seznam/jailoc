package compose

import "strings"

func GenerateCorefile(upstream string, blockedZones []string) string {
	var out strings.Builder
	out.WriteString(". {\n")
	if len(blockedZones) != 0 {
		out.WriteString("    template IN ANY ")
		out.WriteString(strings.Join(blockedZones, " "))
		out.WriteString(" {\n        rcode NXDOMAIN\n    }\n")
	}
	out.WriteString("    forward . ")
	out.WriteString(upstream)
	out.WriteString("\n}\n")
	return out.String()
}
