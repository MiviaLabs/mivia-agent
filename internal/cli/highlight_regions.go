package cli

import "strings"

func findStringRegions(line string, delimiters []string) []strRegion {
	var regions []strRegion
	for _, delim := range delimiters {
		for i := 0; i < len(line); {
			if !strings.HasPrefix(line[i:], delim) {
				i++
				continue
			}
			if i > 0 && line[i-1] == '\\' {
				i++
				continue
			}
			start := i
			i += len(delim)
			for i < len(line) {
				if strings.HasPrefix(line[i:], delim) && (i == 0 || line[i-1] != '\\') {
					regions = append(regions, strRegion{start, i + len(delim)})
					i += len(delim)
					break
				}
				i++
			}
			if i >= len(line) {
				regions = append(regions, strRegion{start, len(line)})
			}
		}
	}
	return mergeRegions(regions)
}

func stringRegionStartingAt(regions []strRegion, pos int) (int, bool) {
	for _, region := range regions {
		if region.start == pos {
			return region.end, true
		}
	}
	return 0, false
}

func positionInString(regions []strRegion, pos int) bool {
	for _, region := range regions {
		if pos >= region.start && pos < region.end {
			return true
		}
	}
	return false
}

func regionsOverlap(regions []strRegion, start, end int) bool {
	for _, region := range regions {
		if start < region.end && end > region.start {
			return true
		}
	}
	return false
}
