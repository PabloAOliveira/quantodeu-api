package handlers

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/dto"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/middlewares"
	"github.com/cgisoftware/quantodeu-api/internal/adapters/http/session"
	"github.com/cgisoftware/quantodeu-api/internal/core/domain"
	"github.com/cgisoftware/quantodeu-api/internal/core/ports"
)

// AuthHandler expõe registro, login, logout, perfil, senha, sessões e
// verificação de telefone.
type AuthHandler struct {
	auth     ports.AuthUseCase
	profile  ports.ProfileUseCase
	verifier ports.PhoneVerificationUseCase
	emailVer ports.EmailVerificationUseCase
	cookies  *session.CookieManager
	log      *slog.Logger
}

// NewAuthHandler cria o handler.
func NewAuthHandler(auth ports.AuthUseCase, profile ports.ProfileUseCase, verifier ports.PhoneVerificationUseCase,
	emailVer ports.EmailVerificationUseCase, cookies *session.CookieManager, log *slog.Logger) *AuthHandler {
	return &AuthHandler{auth: auth, profile: profile, verifier: verifier, emailVer: emailVer, cookies: cookies, log: log}
}

// Register — POST /api/v1/auth/register
func (h *AuthHandler) Register(c *gin.Context) {
	var req dto.RegisterRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	user, err := h.auth.Register(c.Request.Context(), ports.RegisterInput{
		Nome: req.Nome, Email: req.Email, Telefone: req.Telefone,
		Senha: req.Senha, SaldoInicial: req.SaldoInicial.Value,
	})
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusCreated, dto.NewUserResponse(user))
}

// Login — POST /api/v1/auth/login
func (h *AuthHandler) Login(c *gin.Context) {
	var req dto.LoginRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}

	// Se já existir uma sessão, ela é invalidada: login sempre gera token novo.
	if old, _, ok := h.cookies.ReadRequest(c.Request); ok {
		_ = h.auth.Logout(c.Request.Context(), old)
	}

	out, err := h.auth.Login(c.Request.Context(), ports.LoginInput{
		Email: req.Email, Senha: req.Senha,
		UserAgent: c.Request.UserAgent(), IP: c.ClientIP(),
	})
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	h.respondSession(c, out, req.Modo == "token")
}

// respondSession entrega a sessão como cookie (navegador) ou token (apps).
func (h *AuthHandler) respondSession(c *gin.Context, out *ports.LoginOutput, tokenMode bool) {
	c.Header("Cache-Control", "no-store")
	resp := dto.LoginResponse{User: dto.NewUserResponse(out.User), ExpiresAt: out.Session.ExpiresAt}
	if tokenMode {
		resp.Token = h.cookies.SignToken(out.Token)
		resp.TokenType = "Bearer"
	} else {
		h.cookies.Set(c.Writer, out.Token, out.Session.ExpiresAt)
	}
	c.JSON(http.StatusOK, resp)
}

// Logout — POST /api/v1/auth/logout
func (h *AuthHandler) Logout(c *gin.Context) {
	token, bearer, ok := h.cookies.ReadRequest(c.Request)
	if ok {
		if err := h.auth.Logout(c.Request.Context(), token); err != nil {
			respondError(c, h.log, err)
			return
		}
	}
	if !bearer {
		h.cookies.Clear(c.Writer)
		c.Header("Clear-Site-Data", `"cookies"`)
	}
	c.Status(http.StatusNoContent)
}

// Me — GET /api/v1/me
func (h *AuthHandler) Me(c *gin.Context) {
	perfil, err := h.profile.GetProfile(c.Request.Context(), middlewares.UserID(c))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.NewMeResponse(perfil))
}

// ChangePassword — PUT /api/v1/me/senha
//
// Troca a senha, encerra TODAS as sessões (inclusive em outros dispositivos)
// e devolve um cookie novo para este navegador.
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req dto.ChangePasswordRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	out, err := h.auth.ChangePassword(c.Request.Context(), ports.ChangePasswordInput{
		UserID: middlewares.UserID(c), SenhaAtual: req.SenhaAtual, NovaSenha: req.NovaSenha,
		UserAgent: c.Request.UserAgent(), IP: c.ClientIP(),
	})
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	h.respondSession(c, out, middlewares.UsingBearer(c))
}

// RevokeSessions — DELETE /api/v1/me/sessoes?manter_atual=true
func (h *AuthHandler) RevokeSessions(c *gin.Context) {
	var q dto.RevokeSessionsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		respondError(c, h.log, queryError(err))
		return
	}
	keep := ""
	if q.ManterAtual {
		keep, _, _ = h.cookies.ReadRequest(c.Request)
	}
	n, err := h.auth.RevokeSessions(c.Request.Context(), middlewares.UserID(c), keep)
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	if !q.ManterAtual && !middlewares.UsingBearer(c) {
		h.cookies.Clear(c.Writer)
	}
	c.JSON(http.StatusOK, dto.RevokeSessionsResponse{Encerradas: n, ManteveAtual: q.ManterAtual})
}

// StartPhoneVerification — POST /api/v1/me/telefone/verificacao
func (h *AuthHandler) StartPhoneVerification(c *gin.Context) {
	var req dto.StartVerificationRequest
	if c.Request.ContentLength != 0 {
		if err := bindJSON(c, &req); err != nil {
			respondError(c, h.log, err)
			return
		}
	}
	out, err := h.verifier.Start(c.Request.Context(), middlewares.UserID(c), domain.MetodoVerificacao(req.Metodo))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusAccepted, dto.StartVerificationResponse{
		Metodo: string(out.Metodo), ExpiraEm: out.ExpiresAt,
		CodigoParaEnviar: out.CodigoParaEnviar, Instrucao: out.Instrucao,
	})
}

// ConfirmPhoneVerification — POST /api/v1/me/telefone/verificacao/confirmar
func (h *AuthHandler) ConfirmPhoneVerification(c *gin.Context) {
	var req dto.ConfirmVerificationRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	if err := h.verifier.Confirm(c.Request.Context(), middlewares.UserID(c), req.Codigo); err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.MessageResponse{Mensagem: "telefone verificado"})
}

// StartEmailVerification — POST /api/v1/me/email/verificacao
func (h *AuthHandler) StartEmailVerification(c *gin.Context) {
	out, err := h.emailVer.Start(c.Request.Context(), middlewares.UserID(c))
	if err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusAccepted, dto.StartEmailVerificationResponse{
		Email: out.Email, ExpiraEm: out.ExpiresAt,
		Instrucao: "Enviamos um código de 6 dígitos para " + out.Email + ". Confirme em POST /api/v1/me/email/verificacao/confirmar.",
	})
}

// ConfirmEmailVerification — POST /api/v1/me/email/verificacao/confirmar
func (h *AuthHandler) ConfirmEmailVerification(c *gin.Context) {
	var req dto.ConfirmVerificationRequest
	if err := bindJSON(c, &req); err != nil {
		respondError(c, h.log, err)
		return
	}
	if err := h.emailVer.Confirm(c.Request.Context(), middlewares.UserID(c), req.Codigo); err != nil {
		respondError(c, h.log, err)
		return
	}
	c.JSON(http.StatusOK, dto.MessageResponse{Mensagem: "e-mail verificado"})
}
