// Code generated by "stringer -type=MessageStatus -trimprefix MessageStatus"; DO NOT EDIT.

package stream

import "strconv"

func _() {
	// An "invalid array index" compiler error signifies that the constant values have changed.
	// Re-run the stringer command to generate them again.
	var x [1]struct{}
	_ = x[MessageStatusAcked-0]
	_ = x[MessageStatusNacked-1]
	_ = x[MessageStatusOpen-2]
}

const _MessageStatus_name = "AckedNackedOpen"

var _MessageStatus_index = [...]uint8{0, 5, 11, 15}

func (i MessageStatus) String() string {
	if i < 0 || i >= MessageStatus(len(_MessageStatus_index)-1) {
		return "MessageStatus(" + strconv.FormatInt(int64(i), 10) + ")"
	}
	return _MessageStatus_name[_MessageStatus_index[i]:_MessageStatus_index[i+1]]
}
