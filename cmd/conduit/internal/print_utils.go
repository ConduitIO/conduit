package internal

import (
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func Indentation(level int) string {
	return strings.Repeat("  ", level)
}

func PrintStatusFromProtoString(protoStatus string) string {
	return PrettyProtoEnum("STATUS_", protoStatus)
}

func PrettyProtoEnum(prefix, protoEnum string) string {
	return strings.ToLower(
		strings.ReplaceAll(protoEnum, prefix, ""),
	)
}

func PrintTime(ts *timestamppb.Timestamp) string {
	return ts.AsTime().Format("2006-01-02T15:04:05Z")
}
