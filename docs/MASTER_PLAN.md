# Ants — Master plan per un coding-agent open source e commerciale

> **Ants** richiama una colonia di agenti specializzati che pianificano, collaborano e portano a termine lavori complessi. Prima del lancio commerciale servono comunque ricerca marchi, dominio, package name, handle social e confronto linguistico internazionale.

- **Stato:** piano target di prodotto e implementazione; non è una matrice delle capability correnti
- **Data originale:** 22 agosto 2026
- **Ultimo audit del ledger:** 24 agosto 2026
- **Licenza corrente:** Apache-2.0
- **Modello target:** core open source commercialmente utilizzabile; possibili offerte self-hosted e managed, topologia ancora aperta
- **Cliente iniziale ipotizzato:** team software da 2 a 30 persone
- **Release 1 proposta:** da repository a PR pronta, più automazioni, web responsive/PWA, CLI e API

---

## 1. Executive decision

Ants sarà una piattaforma open source per pianificare, eseguire, verificare e coordinare lavoro software di lunga durata. Il prodotto separa deliberatamente:

1. **Intento e planning:** un Planner/Captain read-only esplora il repository, fa domande, costruisce una specifica verificabile e un task graph.
2. **Esecuzione:** Build agent scrivono codice in microVM effimere e worktree/branch isolati.
3. **Ricorsione controllata:** un motore RLM opzionale usa contesto come dati, REPL persistenti e subquery budgetate quando il task lo giustifica.
4. **Integrazione:** un Integrator compone branch dipendenti, rileva conflitti e mantiene la provenienza di ogni modifica.
5. **Verifica:** Review agent, test gate e policy engine decidono se il lavoro può essere presentato come pronto.
6. **Operazioni:** workflow durevoli, recovery, audit, metering, quote e osservabilità rendono il sistema utilizzabile come SaaS e self-hosted.

La prima implementazione deve produrre un **vertical slice realmente eseguibile**, non un grande scaffold di stub. Ogni espansione parte da un percorso end-to-end già verde.

### 1.1 North-star flow

```text
Utente/team
  → collega identità e repository
  → apre un thread e descrive un outcome
  → Planner esplora, chiarisce e produce Spec v1
  → Task Graph sceglie parallelismo o stacking
  → Scheduler assegna una microVM effimera per writer
  → Build agent modifica, testa e committa su branch isolato
  → Integrator compone e risolve dipendenze
  → Review agent + quality gates valutano il diff
  → PR draft/pronta + evidenze + costi + rischi
  → CI/eventi possono riattivare il workflow
  → merge resta umano per default
```

### 1.2 Cosa non costruiremo da zero

Non riscriveremo kernel, VMM, database, identity provider, workflow engine, object store, container runtime, protocollo Git o primitive crittografiche. Li adotteremo dietro confini stabili.

### 1.3 Cosa costituisce il core differenziante

- Modello thread/task/spec/evidence e relative state machine.
- Planner read-only e protocollo di handoff zero-guesswork.
- Task graph ricorsivo con budget, backpressure e cancellation.
- RLM context runtime provider-agnostic.
- Policy engine per tool, rete, segreti, Git e deployment.
- Integrator e shared-reality ledger tra task/thread.
- Ready gate basato su evidenze, non sull’autovalutazione del modello.
- UX operativa per capire cosa sta accadendo, perché e a quale costo.
- Integration SDK coerente e testabile.

---

## 2. Ledger decisionale corrente

Questo ledger corregge la precedente etichetta “decisioni già chiuse”. Le
sezioni successive descrivono una **architettura target**: un componente citato
nel piano non è per questo implementato, approvato dall’utente o selezionato.
Lo stato eseguibile corrente vive in
[`IMPLEMENTATION_BACKLOG.md`](IMPLEMENTATION_BACKLOG.md) e nell’evidenza di
tranche più recente.

### 2.1 Intento confermato dall’utente

| Area | Decisione confermata | Confine |
| --- | --- | --- |
| Nome | Il prodotto si chiama **Ants**, richiamando agenti indipendenti che cooperano | Verifiche marchio, dominio e package restano attività pre-lancio |
| OSS/business | Ants deve essere open source, commercialmente utilizzabile e vendibile | Forma dell’offerta managed, pricing e deployment non sono decisi |
| Orizzonte | Il prodotto finito non deve restare local-only | Nella fase corrente si usano risorse locali, OSS, fake deterministici e servizi usa-e-getta senza costo |
| Core prodotto | Planning profondo, ruolo Captain/Planner, Builder paralleli, isolamento, Review, integrazioni ed esecuzione durevole sono centrali | Capy è un riferimento di principi, non una specifica da copiare |
| RLM | RLM e ricorsione controllata meritano ricerca e un ruolo preciso e misurabile | Spawn generico di subagent non è automaticamente RLM; limiti e valutazione sono obbligatori |
| Strategia OSS | Riutilizzare componenti maturi dietro contratti Ants, mantenendo dominio, UX e policy propri | Nessun assemblaggio nominale di wrapper o dipendenza adottata senza prova |
| Ricerca | Picode, Prime Agent, paper RLM e implementazioni primarie sono riferimenti da studiare | Claim prestazionali o commerciali restano claim upstream finché non riprodotti |
| Mobile | Expo/React Native è una possibile direzione futura | Non è una scelta definitiva né una capability implementata |

### 2.2 Fatti implementati e verificabili nel repository

| Area | Fatto corrente | Limite |
| --- | --- | --- |
| Stack | Go per backend/CLI; TypeScript, Next.js e React per il web; licenza Apache-2.0 | Nessun client Expo e nessun componente Rust |
| API/client | CLI, API `/v1` e console web responsive esistono; OpenAPI genera i contratti TypeScript | La superficie operatore è ancora parziale |
| Persistenza/durabilità | Store memory e PostgreSQL, unit of work, outbox, claim, worker, retry, recovery, cancellation e retention sono implementati | Il motore corrente è Ants; Temporal non è in produzione |
| Identità/tenancy | OIDC bearer, BFF PKCE/cookie sigillato e store tenant-scoped esistono; il bypass dev è rimosso | Membership, inviti, ruoli e authorization fine-grained non esistono |
| Planning/review | Planner e Reviewer deterministici alimentano il vertical slice locale | Non sono agenti model-driven e non esiste un runtime RLM |
| Sandbox/SCM | Driver process/fake e memory/local-Git sono implementati dietro porte | Il process sandbox non è un confine di sicurezza; niente VM reali o lifecycle SCM hosted |
| Quality | Gate Go, contratti, web e suite Docker-backed sono definiti; CI hosted esegue anche PostgreSQL/Keycloak/browser | SBOM, secret scan, vulnerability scan, packaging e deploy gate sono assenti |

### 2.3 Raccomandazioni correnti, non decisioni irreversibili

| Area | Raccomandazione | Prova richiesta prima dell’adozione |
| --- | --- | --- |
| Confini | Conservare porte Ants per provider, workflow, sandbox, SCM, artifact e authorization | Contract/conformance test e blast-radius review |
| Sviluppo | Continuare con vertical slice completi, fake locali canonici e nessun egress implicito | Test negativi, failure injection ed evidenza riproducibile |
| Billing | Costruire prima un ledger locale idempotente di uso e budget | Reconciliation e duplicazione/replay provati prima di un provider di pagamento |
| Integrazioni | Implementare un adapter alla volta contro fake HTTP e fixture registrate | Firma, replay, revoke, rate limit, permessi minimi e audit |
| Isolamento | Definire una conformance suite unica per ogni `SandboxDriver` | Prove separate su macOS e su host Linux/KVM realmente idoneo |

### 2.4 Decisioni aperte

| Area | Alternative da valutare | Perché resta aperta |
| --- | --- | --- |
| Workflow durevole | Rafforzare il motore corrente, migrare dietro le porte, o usare Temporal per un sottoinsieme | Esiste già un motore con claim/outbox; aggiungerne un secondo ha costo di replay, migrazione e operazioni |
| Authorization | Modello Ants/PostgreSQL, OpenFGA, OPA o combinazione | Mancano threat model, membership verticale e confronto operativo |
| Sandbox | vfkit/Virtualization.framework, Firecracker/KVM, Kata/E2B o altri driver | Requisiti piattaforma e hardening non sono stati provati sui relativi host |
| Deployment | Singolo host, servizi separati, Kubernetes o altra topologia | Scala, affidabilità, costi e operazioni non sono ancora misurati |
| Provider AI | Locale, API compatibili o provider hosted | Non esiste ancora una porta/provider verticale né una policy costo/egress approvata |
| Billing | Nessun provider, Stripe o alternativa | Metering e entitlement interni devono precedere settlement esterno |
| Mobile | Nessun client nativo, Expo/React Native o altra soluzione | Il flusso web e i casi d’uso mobile non sono ancora stabilizzati |
| Storage/telemetria | S3-compatible, OpenTelemetry e relativi backend | Retention, privacy, topologia ed esposizione non sono decise |
| Integrazioni | Ordine e ampiezza di GitHub/GitLab/Bitbucket/Slack/Linear/Jira/Vercel/MCP | Servono dipendenze di prodotto e un primo adapter completo, non una lista nominale |

---

## 3. Confini di evidenza: cosa sappiamo di Capy

### 3.1 Confermato da fonti ufficiali

- Un thread è una conversazione durevole che conserva messaggi, macchine, task e PR e può risvegliarsi su CI/review/merge.
- I messaggi a un agent occupato hanno semantiche distinte: interrupt, queue e steer.
- I task sono child agent, possono essere ricorsivi, hanno prompt e strumenti propri e possono usare macchina condivisa per letture o macchina fresca per scritture.
- Il prodotto distingue parallel task disgiunti da stacked task dipendenti.
- Una macchina è un’identità durevole separata dal backing VM usa-e-getta.
- Snapshot e setup script riducono il tempo di ricostruzione dell’ambiente.
- Le automazioni combinano schedule, eventi GitHub, Slack, webhook e on-demand con idempotency, run-as principal e limiti.
- La review produce finding strutturati con categoria, severità, confidenza, file/line e triage.

Fonti:

- [Capy Threads](https://docs.capy.ai/threads)
- [Capy Tasks](https://docs.capy.ai/tasks)
- [Capy Machines](https://docs.capy.ai/machines)
- [Capy Automations](https://docs.capy.ai/automations)
- [Capy Reviews](https://docs.capy.ai/review)
- [Captain vs Build](https://capy.ai/blog/captain-vs-build)
- [April 2026 update](https://capy.ai/blog/april-2026-update)

### 3.2 Claim pubblici di Capy V2, utili come target ma non come baseline provata

Il post/video pubblico di lancio mostra RLM agent ricorsivi, molti subagent asincroni su VM separate, model routing, ambienti fino a 32 vCPU/128 GB e restore da snapshot dichiarato sotto il secondo. I benchmark e i risparmi economici sono claim del vendor: Ants non li ripeterà senza metodologia riproducibile.

- [Post di lancio Capy V2 su X](https://x.com/justinsunyt/status/2087207830202024358)
- [DeepSWE](https://deepswe.datacurve.ai/)

### 3.3 Principio di prodotto estratto, non copiato

Il valore non è “avere molte VM” o “spawnare molti agent”. Il valore è ridurre ambiguità prima della scrittura, isolare gli effetti, rendere durevole l’esecuzione, riportare prove e limitare ricorsione/costo. Ants adotterà questi invarianti con una propria architettura e UX.

---

## 4. Architettura di riferimento

### 4.1 Componenti

```text
Clients
├── Web/PWA (Next.js)
├── CLI (Go)
├── Expo mobile (fase successiva)
└── Public API / webhooks

Edge
├── API gateway
├── OIDC session/token validation
├── webhook ingress + signature/idempotency
├── preview router + signed grants
└── websocket/SSE event stream

Control plane
├── Domain API
├── Thread/Task service
├── Spec service
├── Policy/AuthZ service
├── Integration service
├── Metering/Billing service
├── Artifact service
└── Search/Index service

Durable orchestration
├── Temporal workflows
├── Scheduler/placement
├── Agent runtime coordinator
├── RLM coordinator
├── Review/Integrator workflows
└── Automation scheduler

Data plane
├── Node daemon (Go)
├── Sandbox driver: Firecracker Linux
├── Sandbox driver: vfkit/Virtualization.framework macOS
├── Guest agent via vsock/mTLS
├── Snapshot/cache manager
├── Network/egress policy
└── Port/desktop/file/log streams

State
├── PostgreSQL: dominio, audit, metering, outbox
├── Temporal persistence
├── S3-compatible object storage: snapshot/artifact/log chunks
├── Redis solo per lease/cache/rate limit dimostrati
└── OpenFGA: authorization tuples/model

Observability
├── OpenTelemetry
├── Prometheus metrics
├── Loki logs
├── Tempo traces
└── Grafana dashboards/alerts
```

### 4.2 Regola control plane / data plane

- Il control plane decide **chi può fare cosa, dove e con quale budget**.
- Il node daemon decide **come creare e governare la VM sull’host**.
- Il traffico sandbox/preview non attraversa il domain API.
- Nessun token SCM di lunga durata entra nella VM. Clone, fetch e push passano da un Git credential broker/proxy con grant breve e scope minimo.
- Le VM non possono chiamare il control plane con credenziali tenant generiche. Ricevono workload identity task-scoped con audience e scadenza.

### 4.3 Deployability

Profili supportati:

1. `dev-local`: Postgres, Temporal dev, Keycloak, OpenFGA, object storage locale e un node daemon macOS; un comando.
2. `single-node-linux`: servizi e Firecracker su un host Linux/KVM; utile per self-host e CI.
3. `cluster`: servizi su Kubernetes, node daemon su pool KVM dedicato, scheduler Ants, object store esterno.
4. `managed`: multi-region futuro, control plane regionale, data residency e BYOC.

Non mantenere due motori workflow diversi. Temporal deve funzionare anche in sviluppo; le differenze di profilo sono deployment, non semantica.

---

## 5. Struttura proposta del monorepo

```text
/
├── apps/
│   ├── web/                    # Next.js App Router, dashboard/PWA
│   ├── docs/                   # documentazione prodotto e API
│   └── mobile/                 # Expo Router, aggiunto nella fase mobile
├── cmd/
│   ├── ants/                   # CLI
│   ├── api/                    # public/domain API
│   ├── worker/                 # Temporal activities + agent workflows
│   ├── scheduler/              # placement e capacity
│   ├── node-daemon/            # host lifecycle
│   ├── guest-agent/            # processo dentro VM
│   └── git-proxy/              # credential broker e smart HTTP proxy
├── internal/
│   ├── authn/
│   ├── authz/
│   ├── audit/
│   ├── billing/
│   ├── config/
│   ├── events/
│   ├── integrations/
│   ├── metering/
│   ├── policy/
│   ├── review/
│   ├── rlm/
│   ├── scheduler/
│   ├── sandbox/
│   ├── spec/
│   ├── tasks/
│   └── threads/
├── pkg/
│   ├── api-client-go/
│   ├── integration-sdk/
│   └── sandbox-sdk/
├── packages/
│   ├── api-client-ts/          # generato
│   ├── contracts/              # schema condivisi generati, niente logica dominio
│   ├── design-tokens/
│   ├── integration-testkit/
│   └── ui/                     # componenti web; non importati direttamente da Expo
├── proto/                      # gRPC/Connect internal contracts
├── openapi/                    # public API source of truth
├── db/
│   ├── migrations/
│   ├── queries/
│   └── seeds/
├── deploy/
│   ├── compose/
│   ├── helm/
│   ├── terraform/
│   └── images/
├── test/
│   ├── contract/
│   ├── e2e/
│   ├── fixtures/
│   ├── chaos/
│   └── security/
├── docs/
│   ├── adr/
│   ├── architecture/
│   ├── operations/
│   ├── product/
│   └── threat-model/
├── AGENTS.md
├── PRODUCT.md
├── DESIGN.md
├── SECURITY.md
├── CONTRIBUTING.md
├── GOVERNANCE.md
├── LICENSE
└── Makefile
```

### 5.1 Regole di dipendenza

- `internal/domain` non importa provider, HTTP handler, database driver o SDK esterni.
- Gli adapter implementano port dichiarati dal dominio; non esistono `utils` catch-all.
- Il web importa client generati, non tipi duplicati a mano.
- `packages/contracts` contiene DTO/versioned schemas, non business logic condivisa fra frontend e backend.
- Il node daemon non accede alle tabelle di dominio.
- Ogni integration adapter vive in un package proprio con manifest di capability e testkit.
- Nessuna funzione accetta un `tenant_id` opzionale nei percorsi tenant-scoped.

---

## 6. Modello di dominio e state machine

### 6.1 Entità principali

| Entità | Campi/relazioni essenziali |
| --- | --- |
| `tenant` | id, slug, plan, region, retention policy, status |
| `principal` | human/service, external subject, tenant memberships, status |
| `project` | tenant, repositories, base branches, environment revision, policy set |
| `repository` | SCM provider, installation, remote identity, default branch, permissions |
| `thread` | project, creator, run-as, status, model policy, budget, event cursor |
| `message` | thread, role, delivery mode, content refs, sequence, provenance |
| `spec` | thread, version, assumptions, requirements, non-goals, success criteria, approval |
| `task` | parent thread/task, spec version, dependency edges, placement, depth, budget, state |
| `workspace` | task, repository, branch, base SHA, head SHA, worktree identity |
| `machine` | durable identity, project environment revision, lifecycle state |
| `sandbox` | machine backing, node, driver, resources, snapshot, lease, network policy |
| `snapshot` | tenant/project template, lineage, digests, compatibility, hot/cold status |
| `tool_call` | actor, capability, policy decision, request digest, outcome, cost |
| `artifact` | type, object ref, digest, retention, producer, consumer |
| `evidence` | criterion, source, command, exit code, logs/artifact refs, timestamp |
| `review_round` | head SHA, model route, findings, triage, supersedes |
| `finding` | category, severity, confidence, location, failure scenario, status |
| `integration_connection` | provider, tenant, scopes, secret ref, installation state |
| `automation` | project, prompt, triggers, run-as, budget, enabled, idempotency policy |
| `usage_ledger_entry` | tenant, principal/task, meter, quantity, unit, price snapshot, idempotency key |
| `subscription` | Stripe refs, internal plan, entitlement version, status |
| `audit_event` | immutable actor/action/resource/result, trace id, redacted metadata |

### 6.2 Thread state

```text
idle → planning → awaiting_input → ready_to_execute → executing
executing → waiting_external | needs_attention | reviewing | failed
reviewing → fixing → reviewing
reviewing → ready_for_review
ready_for_review → merged | idle
* → archived (dormant; events accumulate but do not wake)
```

Lo stato visuale è derivato da attività e blocker; non deve essere un flag liberamente incoerente. Gli override umani sono marcati e decadono al nuovo evento operativo.

### 6.3 Task state

```text
draft → queued → provisioning → working → verifying → integrating → done
                 ↘ waiting_external / blocked / cancelled / failed
```

Ogni transizione richiede:

- actor e policy decision;
- expected version per optimistic concurrency;
- idempotency key;
- evento outbox nello stesso commit DB;
- trace id e audit record.

### 6.4 Spec contract

Ogni spec deve contenere:

- outcome utente;
- repository findings con riferimenti;
- assunzioni confermate e non confermate;
- requisiti funzionali e non funzionali;
- file/moduli probabili senza fingere certezza;
- schema dati/API/eventi modificati;
- autorizzazioni e trust boundary;
- failure behavior, retry e rollback;
- edge case e compatibilità;
- test e success criteria osservabili;
- non-goal;
- task graph con motivazione parallel/stacked;
- budget e stop condition.

---

## 7. Agent architecture

### 7.1 Planner/Captain

- Read-only sul codice e sugli strumenti di ricerca.
- Non dispone di write, shell mutativa, push, PR o deploy.
- Può creare draft task e chiedere chiarimenti.
- Deve citare le evidenze del repository che giustificano il piano.
- Non lancia execution finché la spec non soddisfa uno schema e i gate configurati.
- Per modifiche banali può proporre un fast path esplicito.

### 7.2 Build agent

- Riceve spec versionata, subset di contesto, workspace isolato, budget e capability manifest.
- Non eredita implicitamente tutta la conversazione.
- Può editare, testare e fare commit locali.
- Rete, segreti, package install, push e deploy passano dal policy engine.
- Se la spec è insufficiente, produce blocker strutturato; non indovina una decisione di prodotto.

### 7.3 Reviewer

- Analizza il diff all’esatto head SHA.
- Finding = scenario di fallimento + evidenza + severità + confidenza + posizione.
- Distingue `confirmed`, `investigate`, `note`.
- Re-review incrementale, con storico e deduplicazione.
- Loop fix/review ha un massimo configurabile; oltre la soglia torna all’utente con stato e costo, evitando loop infiniti.

### 7.4 Integrator

- Legge task graph, base/head SHA e ownership dei file.
- Applica branch in ordine topologico.
- Non risolve semantic conflict con una scelta cieca: richiede al planner o crea task di integrazione.
- Riesegue i gate interessati dopo composizione.
- Produce manifest `commit → task → spec → evidence`.

### 7.5 Shared reality

Non condividere tutto il contesto. Condividere fatti versionati:

- branch/head e dipendenze;
- API/schema change;
- file ownership temporanea;
- decisioni e supersessioni;
- blocker e finding;
- artifact/evidence refs;
- budget consumato.

Ogni task legge una snapshot coerente del ledger e riceve notifiche solo per eventi rilevanti.

### 7.6 RLM runtime

Ants implementa un RLM come capability, non come nuovo tipo di modello.

```text
RLM Session
├── immutable input handles
├── persistent REPL state
├── typed host functions
├── subquery/subagent calls
├── artifact store
├── evidence ledger
└── budget controller
```

Requisiti:

- input grandi restano fuori dal prompt e sono accessibili per handle;
- REPL in sandbox separata con filesystem e rete policy-scoped;
- output intermedi non entrano automaticamente nel contesto;
- subcall typed, tracciata, cancellabile e idempotente;
- depth, fan-out, concurrency, token, wall time e compute budget;
- backpressure e circuit breaker;
- deterministic replay dei tool event, non delle risposte LLM;
- checkpoint espliciti del REPL e garbage collection;
- prompt injection boundary: contenuto esterno è data, non istruzione;
- fallback al normale agent loop se RLM non porta beneficio o fallisce.

Routing iniziale:

- RLM consigliato: codebase molto grande, ricerca multi-documento, audit di copertura, decomposizione ricorsiva, confronto di molte alternative.
- RLM disabilitato per default: typo, modifica locale, task con pochi file, operazioni ad alto rischio senza decomposizione utile.

---

## 8. Sandbox e microVM

### 8.1 `SandboxDriver` comune

```go
type SandboxDriver interface {
    Capabilities(ctx context.Context) (Capabilities, error)
    Create(ctx context.Context, req CreateRequest) (Sandbox, error)
    Start(ctx context.Context, id SandboxID) error
    Exec(ctx context.Context, id SandboxID, req ExecRequest) (ExecStream, error)
    Snapshot(ctx context.Context, id SandboxID, req SnapshotRequest) (Snapshot, error)
    Pause(ctx context.Context, id SandboxID) error
    Resume(ctx context.Context, id SandboxID) error
    Expose(ctx context.Context, id SandboxID, req PortGrantRequest) (PortGrant, error)
    Destroy(ctx context.Context, id SandboxID) error
}
```

L’interfaccia reale va generata/versionata e deve includere cancellation, request id, tenant/task identity e capability negotiation. Non nascondere differenze non supportabili: una capability assente deve fallire in admission, non a metà task.

### 8.2 Linux/Firecracker

- `/dev/kvm` preflight e host capability report.
- Firecracker jailer, seccomp, cgroup v2, UID/GID dedicati.
- TAP/network namespace per VM, egress firewall, DNS policy e conntrack capacity.
- vsock per guest agent; nessun SSH daemon richiesto.
- block device CoW e snapshot lineage content-addressed.
- guest kernel/rootfs firmati, SBOM e vulnerability scan.
- resource overcommit policy esplicita; hard limit CPU/RAM/disk/PID/I/O.
- node quarantine su errori di isolamento o drift dell’immagine.

### 8.3 macOS/Virtualization.framework

- Driver basato su vfkit/Virtualization.framework per sviluppo Apple Silicon.
- Rootfs Linux ARM64 compatibile con guest-agent e contratti Linux.
- Port forwarding e file transfer via canali controllati, non mount arbitrario della home.
- Feature matrix esplicita rispetto a Firecracker.
- I test macOS provano semantica e integrazione; non valgono come prova del security boundary KVM di produzione.

### 8.4 Snapshot model

```text
Base OS image
  → language/toolchain layer
    → tenant/project environment snapshot
      → per-task CoW disk
```

- Snapshot key = digest base image + arch + kernel + guest-agent + setup revision + repository-independent dependencies.
- Il repository non deve essere incluso per default nello snapshot condiviso.
- Il clone del task usa grant SCM breve.
- Snapshot promotion richiede build riproducibile, health check e compatibility manifest.
- Hot/cold tiering è policy, non logica sparsa nel scheduler.
- Corruzione snapshot deve fare fallback a cold boot e aprire alert, mai produrre un ambiente parzialmente fidato.

### 8.5 Preview ed expose-port

- URL opaco, firmato, con scadenza e revoca.
- ACL tenant/user e opzionale accesso pubblico esplicito.
- Protezione DNS rebinding, host validation, SSRF e reserved ports.
- WebSocket support, body/time limits e bandwidth quota.
- Access log redatto e associato al task.
- Il traffico può mantenere attivo il preview solo se la policy lo consente.

---

## 9. Identity, authorization, policy e secrets

### 9.1 Authentication

- Keycloak come IdP/reference deployment Apache-2.0.
- OIDC Authorization Code + PKCE per web/PWA ed Expo.
- Device Authorization Grant per CLI quando disponibile.
- Service principals per automazioni e integrazioni.
- Session rotation, revoca, CSRF, secure cookies, replay protection.
- Provider linking esplicito; email non è una chiave identità globale affidabile.

### 9.2 Authorization

OpenFGA model iniziale:

```text
tenant: owner/admin/member/billing_viewer
project: admin/developer/viewer
repository: read/write/review
thread: owner/collaborator/viewer
automation: manage/run/view
integration: manage/use
```

- Ogni API fa authn, tenant resolution e authz prima del load sensibile.
- `404` uniforme per risorse inesistenti o non visibili dove serve anti-enumeration.
- Test di non-interferenza cross-tenant obbligatori.

### 9.3 Policy engine

Input minimo:

- actor/run-as;
- tenant/project/task;
- tool/capability e parametri normalizzati;
- risk class;
- network destination;
- secret refs;
- repository/branch;
- budget residuo.

Output:

- allow/deny/require_approval;
- motivazione e policy version;
- constraint ridotti;
- expiry;
- audit redaction rules.

### 9.4 Secrets

- Secret manager adapter: OpenBao/Vault/KMS in produzione; storage cifrato solo per profilo locale.
- Envelope encryption, rotation, version e access audit.
- Secret consegnati just-in-time, task-scoped e mai serializzati nei prompt/eventi/log.
- Redaction deterministica su stdout/stderr, trace e artifact metadata.
- Egress policy e secret scope sono accoppiati: un token GitHub non può essere inviato a host arbitrari.

---

## 10. Integration SDK

### 10.1 Manifest di capability

Ogni adapter dichiara:

- provider e versione;
- auth scheme e scope richiesti;
- risorse/azioni supportate;
- event types;
- idempotency behavior;
- rate-limit model;
- pagination/cursor semantics;
- retryable vs terminal errors;
- webhook verification;
- secret refs;
- data residency/retention note.

### 10.2 Primitives condivise

- OAuth state/PKCE e installation lifecycle.
- Webhook signature, replay window, delivery dedupe.
- Typed errors: unauthorized, forbidden, not_found, conflict, rate_limited, transient, invalid.
- Exponential backoff con jitter e `Retry-After`.
- Outbox/inbox per exactly-once effects logici su delivery at-least-once.
- Token bucket per tenant/provider/installation.
- Fake server e golden contract fixtures.
- Audit e metrics automatici.

### 10.3 Ondate di implementazione

1. **Wave A:** GitHub, generic webhook, MCP.
2. **Wave B:** GitLab e Bitbucket usando SCM port comune.
3. **Wave C:** Slack, Linear e Jira con identity mapping e thread/event semantics.
4. **Wave D:** Vercel e deploy/preview signals.
5. **Wave E:** ulteriori provider solo con use case e maintainer.

La richiesta è di coprire tutte le principali integrazioni, ma “coperta” significa auth, happy path, rate limit, revoke, retry, webhook replay, contract test, docs e UI; un package vuoto non conta.

---

## 11. API e protocolli

### 11.1 Public API

REST/JSON da OpenAPI, versionata sotto `/v1`:

- tenants, memberships, service principals;
- projects, repositories, environments, snapshots;
- threads, messages, specs, tasks, evidence;
- machines, sandboxes, artifacts, previews;
- reviews, findings, pull requests;
- automations, triggers, runs;
- integrations, connections, webhooks;
- usage, budgets, plans, billing portal;
- audit and exports.

Requisiti trasversali:

- idempotency key su ogni mutation remotamente ritentabile;
- cursor pagination stabile;
- ETag/version su update concorrenti;
- RFC 9457 Problem Details o schema equivalente;
- request/trace id;
- rate-limit headers;
- webhook versioning;
- SDK Go/TypeScript generati e contract-tested.

### 11.2 Internal API

- Protobuf + Connect/gRPC per scheduler, node daemon, guest agent e stream.
- mTLS e workload identity.
- Compatibilità N/N-1 durante rolling update.
- Capability negotiation invece di version check sparsi.

### 11.3 Event envelope

```json
{
  "id": "evt_...",
  "type": "task.state.changed.v1",
  "occurred_at": "...",
  "tenant_id": "ten_...",
  "aggregate_id": "tsk_...",
  "aggregate_version": 12,
  "actor": { "type": "service_principal", "id": "sp_..." },
  "trace_id": "...",
  "data": {}
}
```

Eventi immutabili; PII e secret mai inseriti nel payload. Schema registry e compatibility test in CI.

---

## 12. Billing, metering e cost control

### 12.1 Ledger interno

Meter iniziali:

- model input/output/cached/reasoning tokens;
- provider pass-through cost snapshot;
- VM vCPU-second, GiB-RAM-second, disk GiB-hour;
- snapshot/object storage e bandwidth;
- agent/subagent count e duration;
- premium integration executions se previste.

Ogni entry è append-only, idempotente e riconciliabile. I prezzi sono versionati: una run conserva il price book usato.

### 12.2 Budget hierarchy

```text
tenant monthly cap
  → project cap
    → automation/thread cap
      → task/RLM depth/fan-out cap
```

- Soft alert e hard stop distinti.
- Budget reservation prima di workload costosi.
- Release della reservation su cancel/failure.
- Nessun silent model substitution per superare budget.

### 12.3 Stripe

- Stripe Checkout, Customer Portal, subscriptions, invoices e tax-ready boundaries.
- Webhook firmati, inbox dedupe e replay tooling.
- Mapping esplicito fra Stripe customer/subscription/price e tenant/internal plan.
- Stripe non decide authorization in tempo reale; aggiorna entitlement versionato.
- Test con Stripe CLI/fake fixtures; nessuna transazione reale nella sessione gratuita.

---

## 13. Web/PWA e futuro Expo mobile

### 13.1 Modalità UI

Il dashboard è un’interfaccia **Operate**, non una landing page. Priorità:

- capire stato e prossimo owner in pochi secondi;
- vedere spec, task tree, macchina, log, diff, finding, costi e gate;
- intervenire con queue/steer/interrupt/cancel/approve;
- non nascondere errori o attese dietro animazioni.

### 13.2 Surface della Release 1

- Onboarding OIDC, tenant e repository.
- Thread list con needs-attention/ready/active/waiting/idle.
- Thread workspace con composer, plan/spec, task tree e event timeline.
- Machine panel: terminal stream, file/diff, preview e resource usage.
- Review panel con finding/triage e evidence links.
- Automations builder con trigger, run-as, budget e history.
- Integrations settings e health/reconnect.
- Usage/billing/quotas.
- Admin: members, roles, policies, retention, audit.
- Global search e command menu.

### 13.3 Design constraints

- Design system originale; niente clone visuale di Capy/Linear.
- Tastiera first-class, ma nessuna animazione su azioni ad alta frequenza.
- Motion breve, motivata, interruptible e rispettosa di reduced-motion.
- Stato non comunicato solo dal colore.
- WCAG 2.2 AA come floor.
- Empty, loading, degraded, unauthorized, rate-limited, disconnected e partial-failure states disegnati.
- Virtualizzazione per log/timeline grandi; stream con backpressure.
- Mobile responsive reale, non desktop schiacciato.

### 13.4 Expo mobile

Fase successiva con Expo Router:

- `apps/mobile/app` contiene solo route; componenti e servizi fuori dalla directory route.
- OIDC Authorization Code + PKCE, deep link e SecureStore.
- Native tabs/stacks, notifiche push per needs-attention/ready-for-review, background refresh prudente.
- Text importante selectable, tabular nums per metriche, safe-area nativa.
- Expo Go prima; development build solo quando una capability nativa lo richiede.
- Condivisione: client generato, design tokens semantici, query keys e validation schema compatibili.
- Non condividere componenti DOM con React Native e non introdurre WebView per la UI principale.
- Test iOS e Android separati; simulator/export non equivalgono a device proof.

---

## 14. OSS atlas: repository da adottare, studiare o forcare

> Ogni dipendenza deve essere pinnata a commit/tag, verificata con license scanner, SBOM, release cadence, security policy e bus factor. La licenza indicata qui è preliminare: `third_party/manifest.yaml` sarà la fonte verificata prima di importare codice.

### 14.1 VM, sandbox, snapshot e immagini

| Repository | Modalità | Uso proposto |
| --- | --- | --- |
| [firecracker-microvm/firecracker](https://github.com/firecracker-microvm/firecracker) | adopt | VMM Linux/KVM; non forkare senza necessità dimostrata |
| [firecracker-microvm/firecracker-go-sdk](https://github.com/firecracker-microvm/firecracker-go-sdk) | study/embed selettivo | API/lifecycle Go e pattern di jailer |
| [firecracker-microvm/firecracker-containerd](https://github.com/firecracker-microvm/firecracker-containerd) | study | Snapshotter, vsock e integrazione containerd |
| [e2b-dev/infra](https://github.com/e2b-dev/infra) | study/fork-spike | Control/data plane, lazy restore, template build, proxy, placement |
| [e2b-dev/E2B](https://github.com/e2b-dev/E2B) | study | SDK e self-host UX |
| [kata-containers/kata-containers](https://github.com/kata-containers/kata-containers) | study/adopt option | Security hardening, guest agent, container-to-VM semantics |
| [cloud-hypervisor/cloud-hypervisor](https://github.com/cloud-hypervisor/cloud-hypervisor) | evaluate | Backend KVM alternativo e riferimento moderno |
| [crc-org/vfkit](https://github.com/crc-org/vfkit) | adopt/study | Virtualization.framework su Apple Silicon |
| [lima-vm/lima](https://github.com/lima-vm/lima) | study | Networking, images e UX VM macOS |
| [containers/containerd](https://github.com/containerd/containerd) | adopt | Runtime/container image primitives |
| [moby/buildkit](https://github.com/moby/buildkit) | adopt | Build cache e immagini riproducibili |
| [containerd/stargz-snapshotter](https://github.com/containerd/stargz-snapshotter) | evaluate | Lazy image loading |
| [awslabs/soci-snapshotter](https://github.com/awslabs/soci-snapshotter) | evaluate | Lazy OCI snapshot path alternativo |
| [opencontainers/runtime-spec](https://github.com/opencontainers/runtime-spec) | standard | Contratti OCI |
| [opencontainers/image-spec](https://github.com/opencontainers/image-spec) | standard | Immagini e content addressing |

### 14.2 Agent harness, coding loop e RLM

| Repository | Modalità | Uso proposto |
| --- | --- | --- |
| [PrimeIntellect-ai/prime-agent](https://github.com/PrimeIntellect-ai/prime-agent) | study/spike | RLM, persistent REPL, continual harness, programmatic subagent |
| [PrimeIntellect-ai/verifiers](https://github.com/PrimeIntellect-ai/verifiers) | study/adopt tests | Ambienti e valutazione RLM riproducibile |
| [drbillwang/rlm-reproduction](https://github.com/drbillwang/rlm-reproduction) | study | Riproduzione indipendente e limiti RLM |
| [stanfordnlp/dspy](https://github.com/stanfordnlp/dspy) | evaluate | RLM/module e ottimizzazione prompt programmatica |
| [badlogic/pi-mono](https://github.com/badlogic/pi-mono) | study/embed candidate | Provider abstraction, agent core, session tree, RPC, web components |
| [judepayne/picode](https://github.com/judepayne/picode) | study | Planner/Builder/Designer, permissions, scout/worker/reviewer |
| [OpenHands/OpenHands](https://github.com/OpenHands/OpenHands) | study/adopt MIT-only | Action/event model, runtime, evaluation; escludere codice enterprise non MIT |
| [SWE-agent/SWE-agent](https://github.com/SWE-agent/SWE-agent) | study | Agent-computer interface e benchmark harness |
| [Aider-AI/aider](https://github.com/Aider-AI/aider) | study | Repo map, edit format, Git UX e model routing |
| [sst/opencode](https://github.com/sst/opencode) | study | Provider/tooling/session UX e TUI |
| [block/goose](https://github.com/block/goose) | study | Extension/tool model e local agent UX |
| [continuedev/continue](https://github.com/continuedev/continue) | study | Model/provider config, context providers, IDE integration |
| [modelcontextprotocol/servers](https://github.com/modelcontextprotocol/servers) | fixtures/examples | Interoperabilità MCP e security test cases |
| [modelcontextprotocol/typescript-sdk](https://github.com/modelcontextprotocol/typescript-sdk) | adopt | MCP TypeScript adapter |
| [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk) | adopt/evaluate | MCP Go adapter |

### 14.3 Workflow, queue, events e scheduling

| Repository | Modalità | Uso proposto |
| --- | --- | --- |
| [temporalio/temporal](https://github.com/temporalio/temporal) | adopt | Durable execution server |
| [temporalio/sdk-go](https://github.com/temporalio/sdk-go) | adopt | Workflow/activity Go |
| [temporalio/ui](https://github.com/temporalio/ui) | study | Workflow inspection UX, non esporre direttamente ai tenant |
| [nats-io/nats-server](https://github.com/nats-io/nats-server) | evaluate later | High-throughput event fanout; non necessario nel primo slice |
| [riverqueue/river](https://github.com/riverqueue/river) | study/fallback jobs | Pattern Postgres job e uniqueness, non secondo workflow engine |
| [hibiken/asynq](https://github.com/hibiken/asynq) | study only | Retry/scheduling patterns |
| [kubernetes-sigs/controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) | adopt | Controller/operator e reconciliation |
| [kubernetes/client-go](https://github.com/kubernetes/client-go) | adopt | Kubernetes integration |

### 14.4 Identity, authZ, policy e secrets

| Repository | Modalità | Uso proposto |
| --- | --- | --- |
| [keycloak/keycloak](https://github.com/keycloak/keycloak) | adopt service | OIDC/SAML, federation e service identities |
| [openfga/openfga](https://github.com/openfga/openfga) | adopt service/embed client | Relationship-based authorization |
| [open-policy-agent/opa](https://github.com/open-policy-agent/opa) | evaluate/adopt | Policy decision per tool/network/deploy |
| [openbao/openbao](https://github.com/openbao/openbao) | adopt option | Secret manager self-hosted |
| [oauth2-proxy/oauth2-proxy](https://github.com/oauth2-proxy/oauth2-proxy) | study | OIDC edge patterns, non sostituto dell’app auth |
| [lestrrat-go/jwx](https://github.com/lestrrat-go/jwx) | evaluate | JOSE/JWT in Go |

### 14.5 Git, SCM, integrazioni e webhook

| Repository | Modalità | Uso proposto |
| --- | --- | --- |
| [go-git/go-git](https://github.com/go-git/go-git) | evaluate | Operazioni Git pure Go dove la CLI non serve |
| [google/go-github](https://github.com/google/go-github) | adopt | GitHub REST client |
| [shurcooL/githubv4](https://github.com/shurcooL/githubv4) | evaluate | GitHub GraphQL dove necessario |
| [xanzy/go-gitlab](https://github.com/xanzy/go-gitlab) | adopt/evaluate | GitLab client |
| [ktrysmt/go-bitbucket](https://github.com/ktrysmt/go-bitbucket) | evaluate | Bitbucket client; verificare maintenance/API coverage |
| [slack-go/slack](https://github.com/slack-go/slack) | adopt/evaluate | Slack API/socket/webhook |
| [andygrunwald/go-jira](https://github.com/andygrunwald/go-jira) | evaluate | Jira adapter; verificare API cloud attuale |
| [octokit/webhooks](https://github.com/octokit/webhooks) | fixtures/reference | Webhook payload schemas e test cases |
| [stripe/stripe-go](https://github.com/stripe/stripe-go) | adopt | Billing client ufficiale |
| [stripe/stripe-mock](https://github.com/stripe/stripe-mock) | adopt tests | Contract test Stripe senza denaro |

### 14.6 Data, storage, proxy e osservabilità

| Repository | Modalità | Uso proposto |
| --- | --- | --- |
| [postgres/postgres](https://github.com/postgres/postgres) | adopt service | Source of truth relazionale |
| [sqlc-dev/sqlc](https://github.com/sqlc-dev/sqlc) | adopt | Query tipizzate Go |
| [jackc/pgx](https://github.com/jackc/pgx) | adopt | PostgreSQL driver |
| [redis/redis](https://github.com/redis/redis) | optional | Cache/lease/rate limit solo con necessità misurata e licenza verificata |
| [minio/minio](https://github.com/minio/minio) | optional service | S3 self-host; copyleft e licensing review obbligatori |
| [seaweedfs/seaweedfs](https://github.com/seaweedfs/seaweedfs) | evaluate | Object/file storage alternativo |
| [caddyserver/caddy](https://github.com/caddyserver/caddy) | adopt/evaluate | Preview edge e TLS |
| [traefik/traefik](https://github.com/traefik/traefik) | evaluate | Dynamic routing alternativo |
| [open-telemetry/opentelemetry-go](https://github.com/open-telemetry/opentelemetry-go) | adopt | Tracing/metrics/log correlation |
| [open-telemetry/opentelemetry-collector](https://github.com/open-telemetry/opentelemetry-collector) | adopt | Telemetry pipeline |
| [prometheus/prometheus](https://github.com/prometheus/prometheus) | adopt service | Metrics e alert inputs |
| [grafana/grafana](https://github.com/grafana/grafana) | adopt service | Operations dashboard, separato dall’UX tenant |
| [grafana/loki](https://github.com/grafana/loki) | adopt/evaluate | Logs |
| [grafana/tempo](https://github.com/grafana/tempo) | adopt/evaluate | Traces |

### 14.7 Web, design system, testing ed Expo

| Repository | Modalità | Uso proposto |
| --- | --- | --- |
| [vercel/next.js](https://github.com/vercel/next.js) | adopt | Web/PWA |
| [radix-ui/primitives](https://github.com/radix-ui/primitives) | adopt | Accessible primitives |
| [shadcn-ui/ui](https://github.com/shadcn-ui/ui) | study/copy-owned | Base component code da personalizzare, non identità visuale |
| [TanStack/query](https://github.com/TanStack/query) | adopt | Server-state client |
| [TanStack/virtual](https://github.com/TanStack/virtual) | adopt | Timeline/log virtualization |
| [microsoft/monaco-editor](https://github.com/microsoft/monaco-editor) | evaluate | Diff/file viewer avanzato |
| [codemirror/dev](https://github.com/codemirror/dev) | evaluate | Editor più leggero |
| [microsoft/playwright](https://github.com/microsoft/playwright) | adopt | Browser E2E e screenshot |
| [dequelabs/axe-core](https://github.com/dequelabs/axe-core) | adopt | Accessibility automation |
| [expo/expo](https://github.com/expo/expo) | adopt | Mobile runtime |
| [expo/router](https://github.com/expo/router) | adopt/reference | File-based native routing; verificare integrazione corrente in Expo monorepo |
| [software-mansion/react-native-reanimated](https://github.com/software-mansion/react-native-reanimated) | adopt selectively | Motion/gesture nativa quando motivata |
| [maestro-mobile-dev/maestro](https://github.com/mobile-dev-inc/Maestro) | evaluate | Mobile E2E |

### 14.8 Processo per scegliere un upstream

Per ogni candidato:

1. leggere licenza e file di eccezioni;
2. verificare ultime release, maintainer, issue security e dipendenze;
3. costruire uno spike con acceptance test Ants;
4. misurare performance e failure behavior;
5. registrare ADR: adopt/embed/adapter/fork/reject;
6. se fork: upstream remote, patch queue piccola, diff dashboard, sync cadence e owner;
7. generare SBOM e NOTICE;
8. nessun copia-incolla senza provenance nel commit.

---

## 15. Quality contract: niente slop code

### 15.1 Regole non negoziabili

- Nessun `TODO`, fake, mock o stub nel runtime production path senza issue e feature flag esplicita.
- Nessun valore infrastrutturale hardcoded: config tipizzata, default motivato, validation all’avvio.
- Nessun `any`, cast cieco o errore ignorato per far passare il compilatore.
- Errori tipizzati con contesto e classification; no string matching per control flow.
- Context cancellation e timeout attraversano ogni I/O e subprocess.
- Nessun retry indiscriminato; retry solo su errori classificati e idempotenti.
- Nessuna abstraction senza almeno due caller reali o una boundary necessaria.
- Nessuna duplicazione di validator/schema tra API e client.
- Commenti spiegano invarianti, sicurezza e scelte non ovvie; non narrano il codice.
- Log strutturati, redatti e correlati; mai secret o prompt sensibili completi.
- Migrazioni forward/backward compatibili con rolling deploy o esplicita maintenance mode.
- Ogni handler tenant-scoped ha test cross-tenant negativo.
- Ogni integrazione ha fake server e failure matrix.
- Ogni modifica UI include loading/error/empty/keyboard/mobile/reduced-motion.

### 15.2 Deslop gate per tranche

Dopo ogni tranche:

1. confrontare solo il diff della tranche con la base corretta;
2. rimuovere wrapper, defensive check anomali, compatibility layer senza caller, commenti ovvi, nesting e duplicazioni;
3. preservare validation ai trust boundary, sicurezza, migrazioni e contratti;
4. rieseguire i test pertinenti;
5. non ampliare il cleanup a codice estraneo.

### 15.3 Definition of Done per feature

- requirement e non-goal tracciati;
- API/schema/event versionati;
- authn/authz/policy coperti;
- happy path e failure path implementati;
- unit + integration + contract + E2E pertinenti verdi;
- race/concurrency test se applicabile;
- migration test se applicabile;
- telemetry e runbook;
- docs utente/operatore;
- threat-model delta;
- deslop review completata;
- diff review umano/agent con finding risolti o accettati esplicitamente;
- nessuna dichiarazione di supporto piattaforma senza prova.

---

## 16. Test strategy e matrice di prove

### 16.1 Piramide

| Livello | Scopo |
| --- | --- |
| Unit | state machine, policy, parsers, budget, placement score, redaction |
| Property/fuzz | event ordering, idempotency, path traversal, webhook parser, config |
| Integration | Postgres/Temporal/OpenFGA/Keycloak/object store reali in container |
| Contract | OpenAPI, protobuf N/N-1, provider fake servers, webhook fixtures |
| Sandbox | create/exec/cancel/snapshot/resume/destroy, quota e network denial |
| E2E | onboarding → thread → plan → task → commit → review → fake PR |
| Chaos | worker restart, node loss, duplicate delivery, DB failover, corrupt snapshot |
| Security | cross-tenant, SSRF, secret leak, symlink escape, command/path injection |
| Performance | boot p50/p95/p99, scheduler throughput, stream lag, object fetch, fan-out |
| UI | Playwright desktop/mobile, axe, keyboard, reduced motion, degraded network |

### 16.2 Piattaforme

- macOS Apple Silicon: driver VF, web, CLI, local stack.
- Linux x86_64 KVM: Firecracker production path.
- Linux arm64 KVM: target dichiarato solo dopo prova reale.
- Browser: Chromium, WebKit e Firefox correnti nella matrice CI.
- Expo futuro: iOS e Android; simulatore ed export riportati separatamente da device fisico.

### 16.3 Evidenza locale attuale

Ambiente osservato il 22 agosto 2026:

- macOS 26.5.2, Apple Silicon/arm64;
- Go, Node, pnpm, Docker, kubectl e Helm presenti;
- vfkit, QEMU, Temporal CLI e psql non rilevati nel PATH;
- `/dev/kvm`/Firecracker non verificabili su macOS.

Quindi nella sessione gratuita locale si possono provare web/control plane, container integration e, dopo disponibilità del runtime, driver macOS. Firecracker Linux deve essere marcato **NOT RUN** finché non viene eseguito su host KVM.

### 16.4 Performance budgets iniziali

Sono target da misurare, non claim:

- API p95 read < 250 ms in locale test environment.
- Event-to-UI p95 < 1 s senza backlog.
- Scheduler decision p95 < 100 ms a capacità disponibile.
- Sandbox warm restore target < 2 s, misurato per arch/size.
- Cancel propagation p95 < 2 s fino al guest process.
- Nessuna perdita/duplicazione di effetto logico su replay Temporal/webhook.
- UI interaction INP < 200 ms sui surface principali.

---

## 17. Security plan

### 17.1 Threat actors

- utente tenant malevolo;
- repository o dependency malevola;
- prompt injection da issue/webhook/web;
- agent che genera comandi pericolosi;
- integration token compromesso;
- node/guest compromesso;
- insider o operatore con accesso eccessivo;
- supply-chain upstream compromessa.

### 17.2 Trust boundaries da modellare

- browser/CLI ↔ edge;
- edge ↔ domain API;
- workflow ↔ node daemon;
- host ↔ guest VM;
- VM ↔ internet/SCM;
- integration ingress ↔ prompt context;
- tenant ↔ tenant;
- billing provider ↔ entitlement ledger;
- object store ↔ snapshot loader.

### 17.3 Gate prima del SaaS beta

- threat model STRIDE/LINDDUN leggero ma concreto;
- hardened Firecracker host guide e reproducible image;
- penetration test interno riproducibile;
- dependency/SBOM/signing/SLSA-oriented provenance;
- backup/restore e disaster recovery drill;
- incident response e security contact;
- tenant deletion/export workflow;
- privacy/DPA data map;
- audit immutable e operator access logs;
- secret rotation e key compromise drill.

SOC 2 non è un badge da dichiarare in anticipo. Il codice deve produrre evidenze utili, ma certificazione e audit sono un workstream separato.

---

## 18. Roadmap di implementazione

### Horizon 0 — Foundation decisions e bootstrap

Deliverable:

- repository Apache-2.0 con governance, security e contribution docs;
- monorepo Go/TypeScript, toolchain pinning, generated contracts;
- Compose locale con Postgres, Temporal, Keycloak, OpenFGA e object storage;
- config schema e secret references;
- CI strict e pre-commit hooks;
- architecture docs/ADR e third-party manifest.

Exit gate: clone pulito → un comando → stack healthy → test verdi, senza credenziali pagate.

### Horizon 1 — Overnight vertical slice gratuito

Ordine tassativo:

1. Domain model minimo tenant/project/thread/spec/task/evidence.
2. OIDC locale reale e OpenFGA authorization.
3. Temporal thread workflow con queue/steer/interrupt e recovery.
4. Local repository adapter + worktree/branch isolation.
5. Deterministic fake model/provider che emette un piano e una patch fixture.
6. Local sandbox driver iniziale con capability interface; nessuna finta pretesa VM.
7. Build task esegue comando/test fixture e commit locale.
8. Reviewer deterministico produce finding e ready gate.
9. Web: onboarding dev, thread list, thread workspace, task tree, logs/diff/evidence.
10. GitHub adapter contro fake server con create branch/commit/PR/check event.
11. Automation schedule/webhook con idempotency e run-as.
12. Metering ledger e Stripe fake contract.
13. Strict tests, deslop, docs e handoff.

Exit gate: un repository fixture attraversa tutto il flusso e produce commit/diff/review/fake PR riproducibili dopo restart dei worker.

### Horizon 2 — VM reale cross-platform

- vfkit macOS driver e guest agent.
- Firecracker driver su Linux KVM.
- vsock, exec streaming, cancellation, file transfer.
- rootfs/image builder e signing.
- snapshot tenant/project + per-task CoW.
- network policy e port preview signed.
- benchmark harness e failure injection.

Exit gate: stessa conformance suite passa sui due driver; differenze documentate.

### Horizon 3 — Agent + RLM reali

- model gateway API/local/OAuth ufficiale.
- Planner/Builder/Reviewer prompt contracts versionati.
- RLM REPL, input handles, subquery API e budget controller.
- persistent checkpoint e resume.
- model routing e cost estimate.
- evaluation suite con codebase fixture, long-context e regression corpus.

Exit gate: nessun provider obbligatorio; con provider configurato, task reali producono evidence e rispettano budget/cancel.

### Horizon 4 — SCM e collaboration integrations

- GitHub live opt-in.
- GitLab/Bitbucket.
- Slack, Linear, Jira.
- Vercel events/deploy status.
- MCP catalog e scoped servers.
- reconnect/revoke/rate-limit UX.

Exit gate per adapter: auth, core actions, webhook, replay, rate limit, revoke, fake contract, docs e audit.

### Horizon 5 — Commercial beta

- Stripe live test mode, entitlements e billing portal.
- quotas/reservations/cost dashboard.
- Kubernetes deploy, scheduler multi-node e autoscaling.
- backups, DR, retention/deletion/export.
- operations dashboards e alerts.
- PWA installability e mobile responsive polish.
- onboarding self-service e docs.

Exit gate: un team esterno può registrarsi, collegare repo, eseguire automazioni, ricevere PR e capire/limitare costi senza intervento del founder.

### Horizon 6 — Expo mobile e enterprise seams

- Expo iOS/Android per monitoring, steering, approval e review.
- push notifications e deep links.
- BYOC, region/data residency, SCIM e SAML hardening.
- dedicated pools/confidential compute evaluation.
- audit export/SIEM e policy packs.

---

## 19. Backlog tecnico iniziale, in ordine

### P0: necessario per non rifare tutto

- ADR 0001 boundaries e dependency rule.
- ADR 0002 Temporal come unico durable engine.
- ADR 0003 sandbox driver/capability negotiation.
- ADR 0004 tenant scoping/OpenFGA.
- ADR 0005 event/outbox/idempotency.
- ADR 0006 provider/integration SDK.
- ADR 0007 evidence/ready gate.
- ADR 0008 license/reuse/fork policy.
- Config schema e validation.
- Test fixture repository.
- Error taxonomy e Problem Details.
- OpenAPI/proto generation pipeline.

### P1: vertical slice

- migrations/domain repositories;
- OIDC dev login e membership;
- thread/task workflow;
- local Git workspace;
- deterministic agent fixtures;
- execution/evidence/review;
- web event stream e core UI;
- GitHub fake;
- automation webhook/schedule;
- usage ledger/Stripe fake.

### P2: runtime reale

- guest protocol;
- macOS driver;
- Firecracker driver;
- snapshot/image/network/preview;
- chaos/perf/security harness.

### P3: breadth

- RLM;
- GitLab/Bitbucket;
- Slack/Linear/Jira/Vercel/MCP;
- billing live test mode;
- cluster deployment;
- Expo client.

---

## 20. Failure behavior obbligatorio

| Failure | Comportamento |
| --- | --- |
| Worker muore | Temporal riassegna activity idempotente; nessun doppio effetto |
| Node sparisce | sandbox lease scade; task diventa recovering; nuovo backing solo da checkpoint sicuro |
| Snapshot corrotto | quarantine + cold boot fallback + alert |
| Provider LLM rate limit | backoff con budget/time check; stato waiting visibile |
| Provider ritira modello | run pinned fallisce visibilmente; nessuna sostituzione silenziosa |
| Webhook duplicato | inbox/idempotency converge alla stessa run |
| Git head cambia | optimistic conflict; rebase/stack decision esplicita |
| Review loop supera limite | needs-attention con finding residui, costo e tentativi |
| Budget esaurito | cancel cooperativo, checkpoint, report parziale; niente nuova spesa |
| Auth principal disabilitato | automation disabilitata con motivo; nessun fallback di identità |
| Secret revocato | tool call fallisce classified; secret non compare nei log |
| UI perde connessione | reconnect da event cursor, senza duplicare timeline |
| Migration incompatibile | deploy bloccato dal compatibility test |

---

## 21. Operabilità e SLO iniziali

### SLO beta proposti

- Control API availability 99.9% mensile.
- Durable workflow state loss: 0 accettabile.
- Cross-tenant data leak: 0 accettabile.
- Webhook accepted-to-recorded p99 < 5 s.
- Ready task evidence completeness > 99%.
- Sandbox create success > 99% su nodi healthy.

### Runbook minimi

- node unhealthy/quarantine;
- snapshot restore failure;
- Temporal backlog/stuck workflow;
- webhook storm;
- SCM provider outage/rate limit;
- model provider outage;
- object store degradation;
- database restore;
- leaked/revoked integration token;
- Stripe webhook divergence;
- tenant deletion/export.

---

## 22. Master implementation prompt per la sessione lunga

> Incollare questo prompt nella futura task di implementazione dopo aver creato/selezionato il repository. Aggiornare il path del progetto e allegare questo documento.

```markdown
Voglio implementare Ants seguendo integralmente `docs/MASTER_PLAN.md`.

Obiettivo di questa sessione: produrre il massimo codice production-grade possibile senza spendere denaro e senza azioni esterne irreversibili. Costruisci prima il vertical slice Horizon 0 + Horizon 1; poi continua in ordine finché rimane tempo. Non sacrificare correttezza per quantità di file.

Usa esplicitamente queste skill/plugin, nel momento appropriato:

1. `francesco-engineering-workflow:architect` per confermare boundary, ADR e dependency direction prima del bootstrap.
2. `francesco-engineering-workflow:blast-radius` prima di ogni tranche trasversale o cambio di schema/protocollo.
3. `francesco-engineering-workflow:francesco-mode` come router di qualità/evidenza se disponibile.
4. `francesco-engineering-workflow:diagnosing-bugs` quando un test o una state machine fallisce; niente fix casuali.
5. `francesco-engineering-workflow:deslop` dopo ogni tranche, limitato al diff della tranche; poi riesegui i test.
6. `francesco-engineering-workflow:show-me-your-work` per produrre prove concrete prima di dichiarare una milestone completata.
7. `francesco-engineering-workflow:handoff` alla fine con stato, comandi, evidenze e blocchi reali.
8. [@Emil Design Skills](plugin://emil-design-skills@personal), in particolare `emil-design-eng`, per UX e motion con scopo.
9. [$impeccable](/Users/francescogiannicola/.codex/skills/impeccable/SKILL.md): usa `init`/new-work per PRODUCT.md e DESIGN.md; poi `shape`, `harden`, `audit`, `adapt` e un solo `polish` bounded pass sui surface implementati.
10. `$playwright` e `$accessibility-pass` per browser proof desktop/mobile, keyboard, reduced motion e axe.
11. `$expo:building-native-ui` quando inizia `apps/mobile`; Expo Router, native navigation e Expo Go first.

Regole assolute:

- Leggi completamente ogni SKILL.md prima di applicarla.
- Usa dipendenze OSS mature secondo l’OSS atlas; registra adopt/embed/adapter/fork/reject e licenza.
- Nessun push, deploy, pagamento, provider AI a consumo, installazione globale o messaggio esterno senza consenso esplicito.
- Mock/fake solo ai boundary esterni; il percorso dominio/workflow/database deve essere reale.
- Nessun hardcode di tenant, path, porta, provider, URL o secret. Config tipizzata e validata.
- Nessun `any`, TODO nel production path, catch vuoto, retry generico, errore ignorato o compatibility shim senza caller.
- Usa Postgres, Temporal, Keycloak e OpenFGA reali nel profilo locale.
- Mantieni tenant scoping, authz, idempotency, audit, cancellation e budget dal primo commit.
- Il vertical slice deve sopravvivere al restart del worker.
- Ogni tranche termina con format/lint/typecheck/unit/integration/contract/E2E pertinenti e deslop review.
- Se una prova richiede Linux/KVM, provider live, device o credenziali assenti, marca `BLOCKED` o `NOT RUN`; non trasformarla in PASS.
- Preserva le modifiche utente già presenti e lavora in branch/worktree isolato.

Sequenza:

1. Ispeziona cwd, Git status/head/remotes, AGENTS.md e toolchain.
2. Crea un piano eseguibile collegato alle milestone Horizon 0/1.
3. Crea PRODUCT.md, DESIGN.md, ADR 0001-0008 e third-party manifest essenziali.
4. Bootstrap monorepo e stack locale riproducibile.
5. Implementa il vertical slice in tranche piccole ma end-to-end.
6. Verifica restart/replay/idempotency/cross-tenant e UI responsive.
7. Solo dopo il percorso verde amplia adapter, automazioni e billing fake.
8. Consegna una matrice PASS/FAIL/BLOCKED/NOT RUN, non un riassunto ottimistico.

Definition of Done della sessione:

- clone pulito + comando documentato avvia lo stack;
- repository fixture attraversa tenant/project/thread/spec/task/workspace/execute/review/fake PR;
- worker restart non perde il workflow né duplica effetti;
- cross-tenant test negativo passa;
- UI mostra stato, task, log, diff, finding, evidence e usage;
- GitHub e Stripe passano contract test fake;
- docs e runbook riflettono il codice effettivo;
- nessun gate fallito o non eseguito viene presentato come completato.
```

---

## 23. Decisioni residue non bloccanti

Queste non impediscono di iniziare:

- clearance completa del brand `Ants` prima del lancio commerciale;
- cloud/provider iniziale del managed service;
- object store self-host default dopo license/ops spike;
- OPA embedded vs service dopo policy benchmark;
- editor diff Monaco vs CodeMirror dopo UX/perf spike;
- Redis/NATS solo se i benchmark mostrano necessità;
- modello di pricing e margine target;
- profondità massima RLM di default dopo eval reali;
- ordine preciso fra GitLab e Slack nella Wave B/C in base ai primi design partner.

---

## 24. Criteri per dire che il piano è stato seguito

Il progetto non è “Capy open source” perché somiglia alla UI o avvia una VM. È fedele a questo piano quando:

1. l’ambiguità viene ridotta prima dell’implementazione;
2. ogni writer è isolato e ogni effetto è attribuibile;
3. i workflow riprendono dopo failure senza doppie azioni;
4. RLM e parallelismo hanno budget e stop condition;
5. l’utente vede prove, rischi, costi e prossimo owner;
6. tenancy e policy non sono aggiunte tardive;
7. self-host e cloud usano lo stesso core;
8. il codice resta leggibile, tipizzato, testato e privo di slop;
9. le dipendenze OSS accelerano il prodotto senza cancellarne la manutenibilità;
10. nessun claim supera l’evidenza effettivamente raccolta.

---

## 25. Fonti primarie e letture prioritarie

### Capy

- [Documentation index](https://docs.capy.ai/llms.txt)
- [Threads](https://docs.capy.ai/threads)
- [Tasks](https://docs.capy.ai/tasks)
- [Machines](https://docs.capy.ai/machines)
- [Environment](https://docs.capy.ai/environment)
- [Reviews](https://docs.capy.ai/review)
- [Automations](https://docs.capy.ai/automations)
- [Security](https://docs.capy.ai/admin/security)
- [Captain vs Build](https://capy.ai/blog/captain-vs-build)
- [April update](https://capy.ai/blog/april-2026-update)
- [Capy V2 launch](https://x.com/justinsunyt/status/2087207830202024358)

### RLM e agent harness

- [Prime Intellect: Recursive Language Models](https://www.primeintellect.ai/blog/rlm)
- [Prime Agent](https://www.primeintellect.ai/blog/prime-agent)
- [Prime Agent repository](https://github.com/PrimeIntellect-ai/prime-agent)
- [RLM paper](https://arxiv.org/abs/2512.24601)
- [RLM reproduction](https://arxiv.org/abs/2603.02615)
- [Pi monorepo](https://github.com/badlogic/pi-mono)
- [picode](https://github.com/judepayne/picode)

### Sandbox/infrastructure

- [Firecracker](https://github.com/firecracker-microvm/firecracker)
- [Firecracker production host setup](https://github.com/firecracker-microvm/firecracker/blob/main/docs/prod-host-setup.md)
- [E2B infra architecture](https://github.com/e2b-dev/infra/blob/main/docs/ARCHITECTURE.md)
- [Kata virtualization](https://github.com/kata-containers/kata-containers/blob/main/docs/design/virtualization.md)
- [Temporal](https://github.com/temporalio/temporal)
- [OpenFGA](https://github.com/openfga/openfga)

---

## 26. Primo comando della prossima sessione

Non iniziare dal VMM. Iniziare creando il repository e facendo passare una sola storia completa con fake deterministici ai boundary esterni. Solo dopo sostituire il `SandboxDriver` locale con vfkit e Firecracker mantenendo la stessa conformance suite. Questo massimizza codice utile prodotto nella notte e minimizza il rischio di avere molta infrastruttura non collegata a un prodotto funzionante.
