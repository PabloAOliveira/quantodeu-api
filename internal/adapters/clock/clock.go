// Package clock implementa a porta ports.Clock com o relógio do sistema.
package clock

import "time"

// System é o relógio real.
type System struct{}

// Now devolve o instante atual em UTC.
func (System) Now() time.Time { return time.Now().UTC() }
