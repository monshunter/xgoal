package bugfix

import "errors"

var ErrDivisionByZero = errors.New("division by zero")

func Divide(left, right int) (int, error) {
	if right == 0 {
		return 0, nil
	}
	return left / right, nil
}
