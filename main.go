package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	ChatwootURL       string `json:"chatwoot_url"`
	ChatwootToken     string `json:"chatwoot_token"`
	ChatwootAccountID int    `json:"chatwoot_account_id"`
	ChatwootTeamID    int    `json:"chatwoot_team_id"`

	LLMAPIURL string `json:"llm_api_url"`
	LLMAPIKey string `json:"llm_api_key"`
	LLMModel  string `json:"llm_model"`

	InternalBaseURL  string `json:"internal_base_url"`
	InternalUsername string `json:"internal_username"`
	InternalPassword string `json:"internal_password"`

	DataInicio string `json:"data_inicio"` // Formato YYYY-MM-DD
	DataFim    string `json:"data_fim"`    // Formato YYYY-MM-DD
}

type ChatwootListResponse struct {
	Data struct {
		Payload []Conversation `json:"payload"`
	} `json:"data"`
}

type Conversation struct {
	ID        int     `json:"id"`
	Status    string  `json:"status"`
	CreatedAt int64   `json:"created_at"`
	UpdatedAt float64 `json:"updated_at"`
	Meta      struct {
		Sender struct {
			Name                 string `json:"name"`
			AdditionalAttributes struct {
				Description string `json:"description"`
			} `json:"additional_attributes"`
		} `json:"sender"`
	} `json:"meta"`
}

type Message struct {
	ID          int    `json:"id"`
	Content     string `json:"content"`
	MessageType int    `json:"message_type"`
	CreatedAt   int64  `json:"created_at"`
}

type ChatwootMessagesResponse struct {
	Payload []Message `json:"payload"`
}

type MotivoItem struct {
	ID        int           `json:"id"`
	Descricao string        `json:"descricao"`
	Assuntos  []AssuntoItem `json:"assuntos"`
}

type AssuntoItem struct {
	Descricao  string `json:"descricao"`
	Prioridade string `json:"prioridade"`
}

type InternalPageResponse struct {
	Items []struct {
		ID           int     `json:"id"`
		DataChamado  string  `json:"dataChamado"`
		LinkWhatsapp *string `json:"linkWhatsapp"`
	} `json:"items"`
	TotalPaginas int `json:"totalPaginas"`
}

type InternalClientItem struct {
	CodigoCli string `json:"codigoCli"`
	NomeCli   string `json:"nomeCli"`
}

type LLMOutput struct {
	Descricao string              `json:"descricao"`
	Motivo    string              `json:"motivo"`
	Motivos   []string            `json:"motivos"`
	Assuntos  map[string][]string `json:"assuntos"`
	Rede      string              `json:"rede"`
}

type OpenAIRequest struct {
	Model          string          `json:"model"`
	Messages       []OpenAIMessage `json:"messages"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type InternalTicketPayload struct {
	ClienteSemCadastro bool        `json:"clienteSemCadastro"`
	CodigoCliente      string      `json:"codigoCliente"`
	Cnpj               *string     `json:"cnpj"`
	NomePessoaAtendida string      `json:"nomePessoaAtendida"`
	DataAtendimento    string      `json:"dataAtendimento"`
	DataChamado        string      `json:"dataChamado"`
	HoraChamado        string      `json:"horaChamado"`
	DataInicioEfetivo  string      `json:"dataInicioEfetivo"`
	HoraInicioEfetivo  string      `json:"horaInicioEfetivo"`
	DataFinalizacao    string      `json:"dataFinalizacao"`
	HoraFinalizacao    string      `json:"horaFinalizacao"`
	Motivo             string      `json:"motivo"`
	Motivos            []string    `json:"motivos"`
	Assuntos           interface{} `json:"assuntos"`
	Descricao          string      `json:"descricao"`
	LinkWhatsapp       string      `json:"linkWhatsapp"`
	Status             string      `json:"status"`
	LojaEhRede         bool        `json:"lojaEhRede"`
	Rede               *string     `json:"rede"`
}

type Store struct {
	filepath string
	mu       sync.Mutex
	IDs      map[int]bool
}

func NewStore(filepath string) (*Store, error) {
	store := &Store{
		filepath: filepath,
		IDs:      make(map[int]bool),
	}
	if file, err := os.ReadFile(filepath); err == nil {
		var raw struct {
			ProcessedIDs []int `json:"processed_ids"`
		}
		if err := json.Unmarshal(file, &raw); err == nil {
			for _, id := range raw.ProcessedIDs {
				store.IDs[id] = true
			}
		}
	}
	return store, nil
}

func (s *Store) Contains(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.IDs[id]
}

func (s *Store) Add(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.IDs[id] = true

	var idList []int
	for k := range s.IDs {
		idList = append(idList, k)
	}

	raw := struct {
		ProcessedIDs []int `json:"processed_ids"`
	}{
		ProcessedIDs: idList,
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.filepath, data, 0644)
}

var (
	cnpjPattern       = regexp.MustCompile(`\b\d{2}\.?\d{3}\.?\d{3}/?\d{4}-?\d{2}\b`)
	chatwootIDFromURL = regexp.MustCompile(`/conversations/(\d+)`)
	brTz              *time.Location
)

func init() {
	var err error
	brTz, err = time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		brTz = time.FixedZone("BRT", -3*3600)
	}
}

func extractCNPJ(conv Conversation, messages []Message) (string, bool) {
	reNonDigits := regexp.MustCompile(`\D`)

	for _, msg := range messages {
		if msg.MessageType == 2 || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		matches := cnpjPattern.FindAllString(msg.Content, -1)
		for _, match := range matches {
			cleaned := reNonDigits.ReplaceAllString(match, "")
			if len(cleaned) == 14 {
				return cleaned, true
			}
		}
	}

	if desc := conv.Meta.Sender.AdditionalAttributes.Description; desc != "" {
		matches := cnpjPattern.FindAllString(desc, -1)
		for _, match := range matches {
			cleaned := reNonDigits.ReplaceAllString(match, "")
			if len(cleaned) == 14 {
				return cleaned, true
			}
		}
	}

	return "", false
}

func sortMessagesAscending(messages []Message) {
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].CreatedAt < messages[j].CreatedAt
	})
}

func extractFirstAgentMessageTime(conv Conversation, messages []Message) time.Time {
	sortMessagesAscending(messages)

	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) == "" {
			continue
		}

		if msg.MessageType == 1 {
			if msg.CreatedAt > 0 {
				return time.Unix(msg.CreatedAt, 0).In(brTz)
			}
		}
	}
	return time.Unix(conv.CreatedAt, 0).In(brTz)
}

func fetchClientCodeByCNPJ(client *http.Client, cfg Config, cnpj string) (string, bool) {
	url := fmt.Sprintf("%s/api/clientes?cnpj=%s", cfg.InternalBaseURL, cnpj)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[AVISO CLIENTE] Erro HTTP ao buscar CNPJ %s: %v", cnpj, err)
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("[AVISO CLIENTE] CNPJ %s não encontrado no cadastro interno (HTTP %d)", cnpj, resp.StatusCode)
		return "", false
	}

	var clientes []InternalClientItem
	if err := json.NewDecoder(resp.Body).Decode(&clientes); err != nil {
		log.Printf("[AVISO CLIENTE] Erro ao parsear JSON de clientes para CNPJ %s: %v", cnpj, err)
		return "", false
	}

	if len(clientes) == 0 || clientes[0].CodigoCli == "" {
		log.Printf("[AVISO CLIENTE] Retorno vazio para o CNPJ %s no cadastro interno", cnpj)
		return "", false
	}

	return clientes[0].CodigoCli, true
}

func main() {
	configFile, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatalf("Erro ao carregar config.json: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(configFile, &cfg); err != nil {
		log.Fatalf("Erro ao ler JSON de configuracao: %v", err)
	}

	store, err := NewStore("processed.json")
	if err != nil {
		log.Fatalf("Erro ao carregar processed.json: %v", err)
	}

	if cfg.DataInicio == "" {
		cfg.DataInicio = time.Now().In(brTz).Format("2006-01-02")
	}
	if cfg.DataFim == "" {
		cfg.DataFim = time.Now().In(brTz).Format("2006-01-02")
	}

	tInicio, err := time.ParseInLocation("2006-01-02", cfg.DataInicio, brTz)
	if err != nil {
		log.Fatalf("Formato inválido para data_inicio (use YYYY-MM-DD): %v", err)
	}

	tFim, err := time.ParseInLocation("2006-01-02", cfg.DataFim, brTz)
	if err != nil {
		log.Fatalf("Formato inválido para data_fim (use YYYY-MM-DD): %v", err)
	}
	tFim = tFim.Add(23*time.Hour + 59*time.Minute + 59*time.Second)

	jar, err := cookiejar.New(nil)
	if err != nil {
		log.Fatalf("Erro ao criar gerenciador de cookies: %v", err)
	}

	httpClient := &http.Client{
		Timeout: 45 * time.Second,
		Jar:     jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	log.Println("=== MODO PRODUÇÃO ATIVADO ===")
	log.Printf("Janela de Operação Definida: De %s 00:00:00 até %s 23:59:59\n", cfg.DataInicio, cfg.DataFim)

	log.Println("1. Autenticando no sistema interno...")
	if err := loginInternalSystem(httpClient, cfg); err != nil {
		log.Printf("Aviso: Falha no login do sistema interno (%v).", err)
	} else {
		log.Println("✔ Login efetuado com sucesso!")
	}

	log.Println("2. Varrendo sistema interno para alimentar o processed.json no período...")
	sincronizados, err := syncInternalRange(httpClient, cfg, store, cfg.DataInicio, cfg.DataFim)
	if err == nil {
		log.Printf("✔ Sincronização concluída! %d chamados da janela já mapeados e salvos no processed.json.", sincronizados)
	} else {
		log.Printf("Aviso: Falha na sincronização inicial da API interna (%v).", err)
	}

	log.Println("3. Coletando motivos e assuntos do sistema interno...")
	motivos, err := fetchInternalMotivos(httpClient, cfg)
	if err != nil {
		log.Printf("Aviso: Não foi possível obter motivos (%v).", err)
	}

	log.Println("4. Coletando lista de redes do sistema interno...")
	redes, err := fetchInternalRedes(httpClient, cfg)
	if err != nil {
		log.Printf("Aviso: Não foi possível obter redes (%v).", err)
	}

	log.Println("5. Buscando conversas resolvidas no Chatwoot...")
	conversas, err := fetchChatwootConversations(httpClient, cfg, tInicio, tFim)
	if err != nil {
		log.Fatalf("Falha ao consultar Chatwoot API: %v", err)
	}

	log.Printf("Analisando lote de %d conversas resolvidas no período...\n", len(conversas))

	totalCadastrados := 0

	for i, conv := range conversas {
		log.Printf("\n--- [%d/%d] Analisando Conversa ID: %d ---", i+1, len(conversas), conv.ID)

		if store.Contains(conv.ID) {
			log.Printf("[IGNORADO] Conversa ID %d já cadastrada (no processed.json ou na API).", conv.ID)
			continue
		}

		messages, err := fetchMessages(httpClient, cfg, conv.ID)
		if err != nil {
			log.Printf("[ERRO] Falha ao carregar mensagens da conversa %d: %v", conv.ID, err)
			continue
		}

		cnpj, found := extractCNPJ(conv, messages)
		if !found {
			log.Printf("[IGNORADO] Conversa %d sem CNPJ identificado.", conv.ID)
			continue
		}

		codigoCliente, clienteEncontrado := fetchClientCodeByCNPJ(httpClient, cfg, cnpj)
		if clienteEncontrado {
			log.Printf("[CLIENTE] CNPJ %s associado ao Código de Cliente: %s", cnpj, codigoCliente)
		} else {
			log.Printf("[CLIENTE] CNPJ %s não cadastrado na base interna. Marcado como 'clienteSemCadastro'.", cnpj)
		}

		log.Printf("[SUCESSO] CNPJ %s localizado na conversa %d. Gerando resumo via LLM...", cnpj, conv.ID)

		llmOut, err := generateLLMDataWithRetry(httpClient, cfg, messages, motivos, redes)
		if err != nil {
			log.Printf("[ERRO LLM] Falha ao gerar payload via IA para conversa %d: %v", conv.ID, err)
			continue
		}

		ticketPayload := buildTicketPayload(conv, messages, cnpj, codigoCliente, llmOut, cfg)
		logPayload(conv.ID, ticketPayload)

		log.Printf("[POST] Enviando chamado ID %d para a API interna...", conv.ID)
		err = postInternalTicket(httpClient, cfg, ticketPayload)
		if err != nil {
			log.Printf("[ERRO POST] Falha ao cadastrar chamado %d na API interna: %v", conv.ID, err)
			continue
		}

		if err := store.Add(conv.ID); err != nil {
			log.Printf("[AVISO STORE] Falha ao gravar ID %d no processed.json: %v", conv.ID, err)
		} else {
			log.Printf("✔ [SUCESSO PRODUÇÃO] Chamado %d cadastrado com sucesso no sistema interno e salvo no processed.json!", conv.ID)
		}

		totalCadastrados++

		time.Sleep(18 * time.Second)
	}

	log.Printf("\n=== PROCESSO DE PRODUÇÃO CONCLUÍDO (%d Chamados Cadastrados) ===", totalCadastrados)
}

func syncInternalRange(client *http.Client, cfg Config, store *Store, dataInicio, dataFim string) (int, error) {
	pagina := 1
	novosSincronizados := 0

	for {
		url := fmt.Sprintf("%s/api/atendimentos/pagina?escopo=todos&pagina=%d&tamanhoPagina=50", cfg.InternalBaseURL, pagina)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return novosSincronizados, err
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			return novosSincronizados, fmt.Errorf("status %d", resp.StatusCode)
		}

		var pageResp InternalPageResponse
		err = json.NewDecoder(resp.Body).Decode(&pageResp)
		resp.Body.Close()
		if err != nil {
			return novosSincronizados, err
		}

		if len(pageResp.Items) == 0 {
			break
		}

		passouDaJanela := false
		for _, item := range pageResp.Items {
			if item.DataChamado < dataInicio {
				passouDaJanela = true
				continue
			}

			if item.DataChamado >= dataInicio && item.DataChamado <= dataFim {
				if item.LinkWhatsapp != nil && *item.LinkWhatsapp != "" {
					matches := chatwootIDFromURL.FindStringSubmatch(*item.LinkWhatsapp)
					if len(matches) > 1 {
						if id, err := strconv.Atoi(matches[1]); err == nil {
							if !store.Contains(id) {
								store.Add(id)
								novosSincronizados++
							}
						}
					}
				}
			}
		}

		if passouDaJanela || pagina >= pageResp.TotalPaginas {
			break
		}
		pagina++
	}

	return novosSincronizados, nil
}

func fetchChatwootConversations(client *http.Client, cfg Config, tInicio, tFim time.Time) ([]Conversation, error) {
	var conversasValidas []Conversation
	pagina := 1
	maxPaginas := 100 // Garante varredura completa da janela no Chatwoot

	for pagina <= maxPaginas {
		url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations?team_id=%d&status=resolved&assignee_type=me&page=%d&sort_by=last_activity_at_desc",
			cfg.ChatwootURL, cfg.ChatwootAccountID, cfg.ChatwootTeamID, pagina)

		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+cfg.ChatwootToken)

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("status %d", resp.StatusCode)
		}

		var listResp ChatwootListResponse
		err = json.NewDecoder(resp.Body).Decode(&listResp)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		if len(listResp.Data.Payload) == 0 {
			break
		}

		passouDaJanela := false
		for _, c := range listResp.Data.Payload {
			cTime := time.Unix(c.CreatedAt, 0).In(brTz)

			if cTime.After(tFim) {
				continue
			}

			if cTime.Before(tInicio) {
				passouDaJanela = true
				break
			}

			conversasValidas = append(conversasValidas, c)
		}

		if passouDaJanela || len(listResp.Data.Payload) < 20 {
			break
		}
		pagina++
	}

	return conversasValidas, nil
}

func loginInternalSystem(client *http.Client, cfg Config) error {
	loginPayload := map[string]string{
		"usuario": cfg.InternalUsername,
		"senha":   cfg.InternalPassword,
	}

	body, _ := json.Marshal(loginPayload)
	endpoint := fmt.Sprintf("%s/api/auth/login", cfg.InternalBaseURL)

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(out))
	}

	return nil
}

func fetchInternalMotivos(client *http.Client, cfg Config) ([]MotivoItem, error) {
	url := fmt.Sprintf("%s/api/atendimentos/motivos", cfg.InternalBaseURL)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var motivos []MotivoItem
	if err := json.NewDecoder(resp.Body).Decode(&motivos); err != nil {
		return nil, err
	}

	return motivos, nil
}

func fetchInternalRedes(client *http.Client, cfg Config) ([]string, error) {
	url := fmt.Sprintf("%s/api/atendimentos/redes", cfg.InternalBaseURL)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var redes []string
	if err := json.NewDecoder(resp.Body).Decode(&redes); err != nil {
		return nil, err
	}

	return redes, nil
}

func fetchMessages(client *http.Client, cfg Config, conversationID int) ([]Message, error) {
	var allMessages []Message
	beforeID := 0

	for {
		url := fmt.Sprintf("%s/api/v1/accounts/%d/conversations/%d/messages",
			cfg.ChatwootURL, cfg.ChatwootAccountID, conversationID)

		if beforeID > 0 {
			url = fmt.Sprintf("%s?before=%d", url, beforeID)
		}

		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+cfg.ChatwootToken)

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		var msgResp ChatwootMessagesResponse
		err = json.NewDecoder(resp.Body).Decode(&msgResp)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		if len(msgResp.Payload) == 0 {
			break
		}

		allMessages = append(msgResp.Payload, allMessages...)

		firstID := msgResp.Payload[0].ID
		if beforeID == firstID {
			break
		}
		beforeID = firstID

		if len(msgResp.Payload) < 20 {
			break
		}
	}

	sortMessagesAscending(allMessages)
	return allMessages, nil
}

func generateLLMDataWithRetry(client *http.Client, cfg Config, messages []Message, motivos []MotivoItem, redes []string) (*LLMOutput, error) {
	maxRetries := 3
	backoffDelays := []time.Duration{35 * time.Second, 65 * time.Second, 90 * time.Second}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		out, err := generateLLMData(client, cfg, messages, motivos, redes)
		if err == nil {
			return out, nil
		}

		if strings.Contains(err.Error(), "429") {
			delay := backoffDelays[attempt-1]
			log.Printf("[RATE LIMIT] Cota excedida (tentativa %d/%d). Aguardando %v para liberar cota...", attempt, maxRetries, delay)
			time.Sleep(delay)
			continue
		}

		return nil, err
	}

	return nil, fmt.Errorf("excedido o número máximo de tentativas na LLM devido ao Rate Limit")
}

func generateLLMData(client *http.Client, cfg Config, messages []Message, motivos []MotivoItem, redes []string) (*LLMOutput, error) {
	var transcript strings.Builder
	for _, m := range messages {
		if m.MessageType == 2 || m.Content == "" {
			continue
		}
		role := "Cliente"
		if m.MessageType == 1 {
			role = "Atendente"
		}
		transcript.WriteString(fmt.Sprintf("%s: %s\n", role, m.Content))
	}

	var catalogBuilder strings.Builder
	for _, m := range motivos {
		catalogBuilder.WriteString(fmt.Sprintf("- Motivo: \"%s\"\n", m.Descricao))
		if len(m.Assuntos) > 0 {
			catalogBuilder.WriteString("  Assuntos permitidos: ")
			var subs []string
			for _, a := range m.Assuntos {
				subs = append(subs, fmt.Sprintf("\"%s\"", a.Descricao))
			}
			catalogBuilder.WriteString(strings.Join(subs, ", ") + "\n")
		} else {
			catalogBuilder.WriteString("  Assuntos permitidos: Nenhum\n")
		}
	}

	redesFormatadas := strings.Join(redes, ", ")

	systemPrompt := fmt.Sprintf(`Você é um analista de suporte técnico.
Analise a transcrição do atendimento e retorne ESTRITAMENTE um JSON com este schema:
{
  "descricao": "Resumo objetivo do problema e solução aplicada (máx 3 frases)",
  "motivo": "Nome do primeiro motivo principal",
  "motivos": ["Motivo1", "Motivo2"],
  "assuntos": {
    "Motivo1": ["Assunto1_A"]
  },
  "rede": "NomeDaRedeOuNull"
}

Regras Estritas de Seleção:
1. MOTIVO REAL DO ATENDIMENTO: O campo "motivo" e "motivos" DEVEM identificar o problema/solicitação real do cliente (ex: "Nota fiscal de saída", "SNGPC", "Atualização do sistema"). NÃO adicione o motivo "Inatividade" se o cliente apresentou uma dúvida ou problema técnico.
2. ENCERRAMENTO POR INATIVIDADE: Se o atendimento foi encerrado porque o cliente parou de responder (ficou mais de 25/30 minutos inativo ou o atendente informou o encerramento por falta de retorno), mantenha os motivos e assuntos normais do problema do cliente e OBRIGATORIAMENTE adicione no final do texto da "descricao" a frase exata: "Cliente inativo por mais de 30 minutos, conversa encerrada.".
3. MOTIVO "INATIVIDADE" (EXCEÇÃO): Use o motivo "Inatividade" APENAS se o cliente não tiver apresentado NENHUMA dúvida ou solicitação durante todo o histórico do chat.
4. MÚLTIPLOS MOTIVOS E ASSUNTOS: Se o cliente tratou de mais de um assunto técnico, inclua todos no array "motivos" e seus respectivos itens em "assuntos".
5. REGRA DE OURO ("Pedido por Comportamento"): Se a conversa mencionar qualquer interação sobre "Pedido por Comportamento" (oferta, uso, dúvida, agendamento, recusa ou repasse de contato do setor de compras), você DEVE OBRIGATORIAMENTE incluir o motivo "Pedido por Comportamento" no array "motivos" e associar o seu respectivo assunto em "assuntos".
6. Cada assunto retornado DEVE pertencer à lista de assuntos permitidos para aquele motivo específico no catálogo. Se não houver assuntos na lista, retorne [].
7. "rede": Verifique se o nome do cliente ou o diálogo menciona alguma destas redes: [%s]. Se identificar, retorne o nome exato. Caso contrário, retorne null.

CATÁLOGO OFICIAL DE MOTIVOS E ASSUNTOS:
%s`, redesFormatadas, catalogBuilder.String())

	payload := OpenAIRequest{
		Model:          cfg.LLMModel,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
		Messages: []OpenAIMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: transcript.String()},
		},
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", cfg.LLMAPIURL, bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("erro LLM %d: %s", resp.StatusCode, string(out))
	}

	var aiResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&aiResp); err != nil {
		return nil, err
	}

	if len(aiResp.Choices) == 0 {
		return nil, fmt.Errorf("resposta vazia da LLM")
	}

	var output LLMOutput
	if err := json.Unmarshal([]byte(aiResp.Choices[0].Message.Content), &output); err != nil {
		return nil, fmt.Errorf("erro ao parsear JSON da LLM: %v", err)
	}

	return &output, nil
}

func buildTicketPayload(conv Conversation, messages []Message, cnpj string, codigoCliente string, llmOut *LLMOutput, cfg Config) InternalTicketPayload {
	createdTime := time.Unix(conv.CreatedAt, 0).In(brTz)
	updatedTime := time.Unix(int64(conv.UpdatedAt), 0).In(brTz)
	effectiveStartTime := extractFirstAgentMessageTime(conv, messages)

	link := fmt.Sprintf("%s/app/accounts/%d/team/%d/conversations/%d",
		cfg.ChatwootURL, cfg.ChatwootAccountID, cfg.ChatwootTeamID, conv.ID)

	var assuntosPayload interface{}
	if llmOut.Assuntos != nil && len(llmOut.Assuntos) > 0 {
		assuntosPayload = llmOut.Assuntos
	} else {
		assuntosPayload = struct{}{}
	}

	var redePtr *string
	lojaEhRede := false
	if llmOut.Rede != "" && strings.ToLower(llmOut.Rede) != "null" {
		redePtr = &llmOut.Rede
		lojaEhRede = true
	}

	clienteSemCadastro := (codigoCliente == "")

	return InternalTicketPayload{
		ClienteSemCadastro: clienteSemCadastro,
		CodigoCliente:      codigoCliente,
		Cnpj:               &cnpj,
		NomePessoaAtendida: conv.Meta.Sender.Name,
		DataAtendimento:    createdTime.Format("2006-01-02"),
		DataChamado:        createdTime.Format("2006-01-02"),
		HoraChamado:        createdTime.Format("15:04"),
		DataInicioEfetivo:  effectiveStartTime.Format("2006-01-02"),
		HoraInicioEfetivo:  effectiveStartTime.Format("15:04"),
		DataFinalizacao:    updatedTime.Format("2006-01-02"),
		HoraFinalizacao:    updatedTime.Format("15:04"),
		Motivo:             llmOut.Motivo,
		Motivos:            llmOut.Motivos,
		Assuntos:           assuntosPayload,
		Descricao:          llmOut.Descricao,
		LinkWhatsapp:       link,
		Status:             "RESOLVIDO",
		LojaEhRede:         lojaEhRede,
		Rede:               redePtr,
	}
}

func postInternalTicket(client *http.Client, cfg Config, ticket InternalTicketPayload) error {
	body, err := json.Marshal(ticket)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/api/atendimentos", cfg.InternalBaseURL)
	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status HTTP %d: %s", resp.StatusCode, string(out))
	}

	return nil
}

func logPayload(conversationID int, ticket InternalTicketPayload) {
	prettyJSON, err := json.MarshalIndent(ticket, "", "  ")
	if err != nil {
		log.Printf("Erro ao serializar payload: %v", err)
		return
	}

	fmt.Println("==================================================")
	fmt.Printf("PAYLOAD FINAL GERADO (CHAMADO ID: %d)\n", conversationID)
	fmt.Println("==================================================")
	fmt.Println(string(prettyJSON))
	fmt.Println("==================================================")
}
