package domain

import "time"

// MensagemRecebida é a representação NEUTRA de uma mensagem de WhatsApp,
// independente do provedor (Evolution API, Z-API, Twilio...). Cada adaptador
// de webhook converte o payload proprietário para esta estrutura.
type MensagemRecebida struct {
	Provider   string
	MessageID  string
	Telefone   string // como veio do provedor; o serviço normaliza
	Texto      string
	FromMe     bool
	IsGroup    bool
	RecebidaEm time.Time
}

// LancamentoInterpretado é o resultado do parser de linguagem natural.
type LancamentoInterpretado struct {
	Tipo      TipoTransacao
	Valor     Money
	Categoria string
	Descricao string
}

// StatusProcessamento descreve o desfecho do processamento de um webhook.
type StatusProcessamento string

const (
	StatusCriada           StatusProcessamento = "criada"
	StatusDuplicada        StatusProcessamento = "duplicada"
	StatusIgnorada         StatusProcessamento = "ignorada" // fromMe, grupo, sem texto
	StatusUsuarioNaoAchado StatusProcessamento = "usuario_nao_encontrado"
	StatusNaoInterpretada  StatusProcessamento = "nao_interpretada"
	StatusConsulta         StatusProcessamento = "consulta_saldo"

	StatusTelefoneNaoVerificado StatusProcessamento = "telefone_nao_verificado"
	StatusEmailNaoVerificado    StatusProcessamento = "email_nao_verificado"
	StatusVerificacaoConfirmada StatusProcessamento = "verificacao_confirmada"
	StatusVerificacaoInvalida   StatusProcessamento = "verificacao_invalida"
)

// ResultadoWebhook é devolvido pelo caso de uso de webhook.
type ResultadoWebhook struct {
	Status    StatusProcessamento
	Transacao *Transacao
}
