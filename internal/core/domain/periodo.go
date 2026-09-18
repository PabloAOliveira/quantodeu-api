package domain

import "time"

// Periodo representa um mês de competência [Inicio, Fim).
type Periodo struct {
	Ano    int
	Mes    time.Month
	Inicio time.Time // primeiro dia do mês 00:00
	Fim    time.Time // primeiro dia do mês seguinte 00:00 (exclusivo)
}

// NewPeriodo valida mês/ano e monta o intervalo no fuso informado.
func NewPeriodo(ano, mes int, loc *time.Location) (Periodo, error) {
	if mes < 1 || mes > 12 {
		return Periodo{}, NewValidationError("mes", "deve estar entre 1 e 12")
	}
	if ano < 2000 || ano > 2100 {
		return Periodo{}, NewValidationError("ano", "deve estar entre 2000 e 2100")
	}
	if loc == nil {
		loc = time.UTC
	}
	inicio := time.Date(ano, time.Month(mes), 1, 0, 0, 0, 0, loc)
	return Periodo{Ano: ano, Mes: time.Month(mes), Inicio: inicio, Fim: inicio.AddDate(0, 1, 0)}, nil
}

// PeriodoDe devolve o mês de competência que contém t.
func PeriodoDe(t time.Time) Periodo {
	p, _ := NewPeriodo(t.Year(), int(t.Month()), t.Location())
	return p
}

// AddMonthsClamped soma n meses a uma data preservando o dia quando possível
// e usando o último dia do mês quando não existir (ex.: 31/01 + 1 = 28/02).
func AddMonthsClamped(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m, 1, 0, 0, 0, 0, t.Location()).AddDate(0, n, 0)
	lastDay := first.AddDate(0, 1, -1).Day()
	if d > lastDay {
		d = lastDay
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, t.Location())
}
