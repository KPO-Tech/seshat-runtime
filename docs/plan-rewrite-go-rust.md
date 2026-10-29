# Plan de réalisation — Seshat Go + Rust

> Statut : plan d'exécution. Phase 0 réalisée le 2026-10-28.
> Date : 2026-10-28
> Base analysée : `seshat-runtime` (Go) et `openai/codex` @ `154e8736` (2026-10-28)

**Écarts entre ce plan et la phase 0 telle qu'elle a été livrée**, à lire avant
le reste :

- **§5 décrit un cadrage protobuf ; l'implémentation utilise du JSON
  length-prefixed**, sans `protoc` ni génération de code. Raison et contrat
  normatif : [sandboxd-protocol.md](sandboxd-protocol.md) et
  [ADR-0001](adr/0001-go-rust-boundary.md). Le reste du §5 (invariants, champs,
  `violations`) tient.
- **Le kill d'arbre de processus est arrivé en phase 0, pas en phase 1.** Sans
  lui, `timeout_ms` ne tenait pas sa promesse sous Windows : `cmd.exe` mourait,
  l'enfant survivait et gardait les tubes ouverts. Il est donc implémenté
  (`taskkill /T` sous Windows, groupe de processus sous Unix) avant d'être
  remplacé par un Job Object.
- `stdin` n'est pas encore dans le protocole : une requête qui en porte est
  refusée, jamais exécutée sans son entrée.
- **§4.2/4.4 (consommer les crates codex) n'est pas encore tranché** : la phase 0
  n'en dépend pas, puisque le sidecar ne confine encore rien. Le spike go/no-go
  reste à faire au début de la phase 1.

---

## 0. Les trois réponses, en clair

**1. Un seul codebase ?** Oui. Un monorepo `seshat-runtime`, avec un espace de travail Cargo dans `rust/` à côté des deux modules Go existants. Un seul numéro de version, une seule release, des binaires embarqués par plateforme.

**2. Quoi en Go, quoi en Rust ?** Rust possède **le bac à sable d'exécution et l'évaluation de politique** — c'est-à-dire tout ce qui décide et applique « cette commande a-t-elle le droit de tourner, et avec quel accès ». Go garde **tout le reste** : boucle d'agent, prompts, providers, outils, sessions, TUI, gRPC, SDK, hooks, MCP, sous-agents, mémoire, orchestration des permissions. Aucun outil métier ne passe en Rust. Aucun prompt ne passe en Rust.

**3. Codex comme base ?** Non — **codex n'est pas une base, c'est une pièce détachée**. 2,19 M de lignes et 122 crates : on ne forke pas ça pour faire Seshat. En revanche on consomme 3 de ses crates (sandbox Linux, moteur de politique, durcissement de processus) et on lit le reste comme référence de design. C'est exactement le rapport que Seshat entretient déjà avec OpenClaude (contrat d'outil) et Crush (TUI).

**On peut commencer.** Les trois premiers commits sont en §9.

---

## 1. La règle d'or, et ce qui en découle

> **Un seul composant possède la boucle d'agent. Tout le reste est serviteur, jamais pair.**

Concrètement, trois conséquences non négociables :

**R1 — Le Rust ne voit jamais un prompt, un modèle, un token.** Il reçoit une commande, un répertoire de travail, une politique, des limites. Il renvoie des octets, un code de sortie, une durée. Il ne connaît ni OpenAI ni Anthropic ni MCP. C'est ce qui rend son périmètre auditable et sa surface d'attaque minuscule.

**R2 — Pas de CGO.** Go tourne aujourd'hui avec `modernc.org/sqlite` (pur Go, sans CGO). On garde cette propriété : la frontière Go↔Rust est **uniquement un processus**. Pas de `cdylib`, pas de bindgen, pas de chaîne d'outils C sur Windows. C'est ce qui permet à `go build` de rester une commande qui marche partout, et c'est aussi la seule frontière qui survit à un crash du composant Rust.

**R3 — Échec fermé.** Si le sidecar ne démarre pas, si le handshake échoue, si la politique est illisible : **on refuse d'exécuter**, on ne retombe jamais silencieusement sur l'exécution hôte. C'est la leçon la plus importante du code existant, où le sandbox est optionnel par défaut (`NoopExecutor` exécute directement sur l'hôte).

---

## 2. Le codebase

### 2.1 Arborescence cible

```
seshat-runtime/
├── go.mod                      # module principal (existant)
├── go.work                     # racine des 2 modules Go
├── cmd/                        # CLI + serveur gRPC (existant)
├── internal/                   # moteur (existant)
├── pkg/                        # API publique + SDK (existant)
├── tui/                        # module Go séparé (existant)
├── proto/
│   ├── seshat.proto            # contrat client (existant, à compléter §6)
│   └── sandboxd.proto          # NOUVEAU — frontière interne Go↔Rust
├── rust/                       # NOUVEAU — workspace Cargo
│   ├── Cargo.toml              # [workspace] members = ["sandboxd", "policy"]
│   ├── rust-toolchain.toml     # version épinglée
│   ├── sandboxd/               # binaire : seshat-sandboxd
│   │   ├── src/main.rs
│   │   ├── src/server.rs       # boucle de frames
│   │   ├── src/backend/linux.rs
│   │   ├── src/backend/windows.rs
│   │   ├── src/backend/macos.rs
│   │   └── src/probe.rs        # capacités au handshake
│   └── policy/                 # bibliothèque : règles + décision
│       └── src/lib.rs
├── docs/
│   └── adr/                    # décisions d'architecture
└── .github/workflows/          # CI Go + CI Rust + release agrégée
```

`seshat-runtime-rs` reste **vide et parqué**. C'est délibéré : le jour où ce dossier commence à contenir un second agent, on a perdu. Il ne se remplit qu'à la porte de décision de phase 4 (§8).

### 2.2 Pourquoi pas de dépôt séparé

Un dépôt séparé pour le sandbox créerait exactement le problème que la règle d'or interdit : deux projets avec deux backlogs, deux versions, et une tentation permanente de faire grossir le second. Un monorepo rend la frontière visible dans l'arborescence, et le versionnage atomique trivial : une release = un couple (Go, Rust) testé ensemble.

Le coût à accepter : la CI doit construire les deux chaînes, et un contributeur Go qui touche au sandbox doit installer Rust. On l'atténue en rendant **le sidecar optionnel à la compilation Go** (variable de build + binaire fourni par la release), tout en le rendant **obligatoire à l'exécution** quand le mode sandbox est demandé.

### 2.3 Versionnement

- Une seule version sémantique pour toute la release.
- Le protocole `sandboxd` porte son propre numéro (`protocol_version`), vérifié au handshake. Un Go récent refuse un sidecar trop ancien, et inversement.
- La matrice de compatibilité vit dans `proto/sandboxd.proto` sous forme de commentaire normatif — pas dans un wiki.

---

## 3. La frontière, fonction par fonction

| Domaine | Aujourd'hui | Cible | Justification |
|---|---|---|---|
| Boucle d'agent (state machine) | Go `internal/engine` | **Go** | I/O-bound, aucun gain Rust ; le porter serait de la traduction pure |
| Prompts / assemblage | Go | **Go** | Change à chaque itération produit |
| Providers (15) | Go | **Go** | Les quirks sont déjà payés ; rien à gagner |
| Contrat d'outil (104) | Go `contract.Tool` | **Go** | API publique, SDK, écosystème |
| Outils métier | Go | **Go** | Écosystème de bibliothèques |
| Sessions SQLite (13 tables) | Go `modernc` | **Go** | CGO-free, testé, migration à chaud impossible |
| TUI | Go (module `tui/`, 71 k) | **Go** | Fonctionne ; 473 k lignes côté codex, on ne réécrit pas ça |
| gRPC / SDK Python | Go | **Go** | Contrat public |
| Hooks, MCP client, sous-agents, mémoire | Go | **Go** | Orchestration, pas d'isolation |
| **Orchestration des permissions** (6 modes) | Go | **Go** | Décide *qui* approuve : logique produit |
| **Évaluation de politique de commande** | n/a | **Rust** | Décide *si* c'est permis : doit être incassable |
| **Exécution sandboxée** | Go `internal/sandbox` (2 377 l.) | **Rust** | Isole : doit être irréfutable |
| **Limites de ressources** | Docker seulement | **Rust** | Doit tenir aussi sans Docker |
| **Probe de capacités** | absent | **Rust** | Doit rapporter honnêtement le niveau réel |

Deux lignes seulement passent en Rust. C'est voulu. **Un plan qui déplace dix lignes est un plan qu'on n'exécute pas.**

### 3.1 Le point de couture exact, côté Go

`internal/sandbox` a déjà la bonne forme :

```go
type Executor interface {
    Run(ctx context.Context, req RunRequest) (RunResult, error)
    Kind() EnvironmentKind
    Healthy(ctx context.Context) error
}
```

et `NewExecutor` fait un `switch` sur `EnvironmentKind` (`local`, `docker`, `remote`).

**La modification Go totale de la phase A :**

1. `types.go` — ajouter `EnvironmentSandboxed EnvironmentKind = "sandboxd"` et `SandboxdConfig{ BinaryPath, PolicyFile, Level, MaxOutputBytes }`.
2. `sandboxd.go` (nouveau, ~350 lignes) — `SandboxdExecutor` : démarrage paresseux du sidecar, handshake, `Run` = envoi de frame + collecte des frames, `Healthy` = handshake, `Close` = arrêt propre.
3. `executor.go` — un `case EnvironmentSandboxed:` dans `NewExecutor`.
4. Le câblage de config là où `ExecutorConfig` est construit.

**Rien d'autre.** Pas d'outil modifié, pas de contrat changé, pas de prompt touché. `internal/tools/bash` continue d'appeler `Executor.Run` sans savoir ce qu'il y a derrière. C'est un backend de plus, pas une architecture nouvelle.

Conséquence agréable : **ce code Go est identique sur les trois OS.** Toute la différence Linux/Windows/macOS vit dans le binaire Rust.

---

## 4. Ce qu'on prend à codex — et ce qu'on ne prend pas

### 4.1 Faits vérifiés sur le clone à jour

| Élément | Mesure |
|---|---|
| Licence | Apache-2.0 (avec `NOTICE`) |
| Workspace | 122 crates, 5 623 fichiers `.rs`, 2 190 406 lignes (tests inclus) |
| Version des crates | `0.0.0`, non publiées → consommation par `git`, pas par `cargo add` |
| Dernier commit du clone | `154e8736`, 2026-10-28 |
| Extensions | `ext/extension-api` + `plugin-host` + `core-plugins` (50 k) — vrai système de plugins |

### 4.2 Les trois pièces retenues

**`codex-linux-sandbox`** — 12 852 lignes. Landlock + seccomp + bubblewrap, avec repli WSLg et proxy réseau. Dépend de 7 crates internes (`protocol`, `sandboxing`, `install-context`, `network-proxy`, `uds`, `absolute-path`, `process-hardening`) — c'est une chaîne, pas un crate isolé, mais on reste loin des 122. **Point important :** son API publique est `run_main() -> !`, c'est-à-dire un **schéma de ré-exécution (arg0)** : le binaire se relance lui-même en mode « lanceur sandboxé ». On adopte ce schéma (`~50` lignes) plutôt que d'espérer une API bibliothèque qui n'existe pas.

**`codex-execpolicy`** — 2 975 lignes, 1 dépendance interne. C'est le langage de règles et le moteur :

```rust
pub enum Decision { Allow, Prompt, Forbidden }
pub use policy::{Policy, Evaluation, MatchOptions};
pub use rule::{Rule, PrefixRule, NetworkRuleProtocol, PatternToken};
pub use parser::PolicyParser;
pub use amend::blocking_append_allow_prefix_rule;   // auto-amendement
```

Enum à trois variantes, exhaustive, sans valeur « par défaut ». C'est précisément le type de contrat qui rend le bug d'auto-mode **inexprimable**. Correspondance directe : `Allow→allow`, `Prompt→ask`, `Forbidden→deny` (les trois valeurs de `sandbox.Decision` chez Seshat).

**`codex-process-hardening`** — zéro dépendance interne. Petites garanties de durcissement au lancement. À consommer ou à recopier, trivial.

### 4.3 Ce qu'on lit sans dépendre

- **`codex-windows-sandbox`** (30 369 lignes) : **ne pas en dépendre en phase A.** Il embarque trois binaires dont `codex-windows-sandbox-setup` avec un manifeste — donc une **élévation** à l'installation — plus un module `elevated/`, `installation_record.rs`, et un service. On lit `acl.rs`, `cap.rs`, `no_reparse_dir.rs`, `hide_users.rs` pour en extraire le sous-ensemble utile, et on réimplémente ce sous-ensemble. Dépendre de ce crate, c'est hériter d'un installeur Windows.
- **`ext/extension-api`** : c'est le modèle de design le plus utile du dépôt. `ToolPolicy` — un plafond posé au démarrage, immuable, que l'état de session ne peut pas relâcher :

```rust
pub struct ToolPolicy {
    pub allowed_tools: Option<Vec<ToolName>>,
    pub require_managed_sandbox: bool,   // omit core tools unless managed sandbox
    pub require_unified_exec: bool,
    pub expose_additional_permissions: bool,
}
```

`require_managed_sandbox` est exactement la primitive de gouvernance qui manque à Seshat : *pas de sandbox géré ⇒ pas d'outils dangereux du tout*. On copie l'idée (pas le crate).
- **`ExtensionRegistry` / `contributors`** (10 natures de contributeurs : prompt, tool, turn, thread, model-request…) : référence pour l'évolution future du système de hooks de Seshat. Hors périmètre ici.

### 4.4 Mécanisme de consommation

Dans `rust/sandboxd/Cargo.toml` :

```toml
[dependencies]
codex-linux-sandbox = { git = "https://github.com/openai/codex", rev = "154e8736", optional = true }
codex-execpolicy    = { git = "https://github.com/openai/codex", rev = "154e8736" }
codex-process-hardening = { git = "https://github.com/openai/codex", rev = "154e8736" }

[target.'cfg(target_os = "linux")'.dependencies]
codex-linux-sandbox = { git = "https://github.com/openai/codex", rev = "154e8736" }
```

Le `rev` est épinglé et **dupliqué dans `rust/UPSTREAM.md`** avec la marche à suivre de mise à jour. Pas de `branch`, pas de `tag` mouvant.

**Plan de repli, décidé d'avance :** si en phase 0 la dépendance git ne compile pas proprement (API interne instable, features par défaut, `[lints] workspace = true` hérité), on **vendore** `linux-sandbox/`, `execpolicy/` et `process-hardening/` dans `rust/vendor/codex/` et on ajoute `scripts/sync-codex.sh` qui documente la copie et le patch appliqué. Apache-2.0 l'autorise explicitement, à condition de conserver `NOTICE` — ce qui est fait dans les deux cas.

Le critère de sortie est mesurable : **`cargo build` vert sur Windows et sur Linux, sans toucher au reste du workspace codex.** C'est un point go/no-go de la phase 0.

---

## 5. Le protocole `sandboxd`

### 5.1 Transport

**stdio uniquement.** Le processus Go lance `seshat-sandboxd` en enfant, parle sur stdin/stdout, et le tue à la fermeture de session. Jamais de port TCP, jamais de socket système, jamais de démon partagé.

Raisons : zéro authentification à concevoir (le canal est privé au couple de processus), zéro découverte de service, zéro conflit de port, et un cycle de vie qui suit exactement celui de la session — ce qui garantit l'absence de processus orphelin (le vrai piège des sidecars).

**Framing :** `u32` big-endian (longueur) + message Protobuf. Le protobuf évite tout échappement — critique pour streamer du stdout qui contient des octets arbitraires, des séquences ANSI et parfois de l'invalid UTF-8.

### 5.2 Messages

```protobuf
syntax = "proto3";
package seshat.sandboxd.v1;

// Version du protocole. Le client refuse toute version majeure différente.
message Hello {
  uint32 protocol_version_major = 1;
  uint32 protocol_version_minor = 2;
  string client_version = 3;
}

message HelloAck {
  uint32 protocol_version_major = 1;
  SandboxLevel level = 2;           // ce qui est RÉELLEMENT appliqué
  repeated Capability capabilities = 3;
  repeated string degraded_reasons = 4;  // vide si level == FULL
}

enum SandboxLevel {
  SANDBOX_LEVEL_UNSPECIFIED = 0;
  SANDBOX_LEVEL_FULL     = 1;  // fs + réseau + ressources + noyau
  SANDBOX_LEVEL_DEGRADED = 2;  // sous-ensemble, motifs listés
  SANDBOX_LEVEL_NONE     = 3;  // refus d'exécuter (échec fermé)
}

enum Capability {
  CAPABILITY_FS_CONFINE = 0;
  CAPABILITY_NETWORK_DENY = 1;
  CAPABILITY_LANDLOCK = 2;
  CAPABILITY_SECCOMP = 3;
  CAPABILITY_JOB_OBJECT = 4;
  CAPABILITY_PTY = 5;
}

message RunRequest {
  string run_id = 1;
  string command = 2;               // argv[0..n] déjà découpé côté Go
  repeated string argv = 3;
  string work_dir = 4;              // absolu, résolu et vérifié
  map<string, string> env = 5;
  repeated string writable_paths = 6;   // au-delà de work_dir : rien
  repeated string readable_paths = 7;   // lecture seule explicite
  bool allow_network = 8;
  uint64 timeout_ms = 9;
  uint64 memory_limit_mb = 10;
  uint64 cpu_ms = 11;
  uint32 max_processes = 12;
  uint64 max_output_bytes = 13;
  string policy_id = 14;            // politique pré-chargée par référence
}

message RunChunk {                   // frame de streaming
  string run_id = 1;
  oneof kind {
    bytes stdout = 2;
    bytes stderr = 3;
    StdinRequest stdin = 4;          // le sidecar réclame de l'entrée (pty)
  }
}

message RunExit {
  string run_id = 1;
  int32 exit_code = 2;
  uint64 duration_ms = 3;
  bool timed_out = 4;
  bool truncated = 5;               // max_output_bytes atteint
  uint64 total_stdout_bytes = 6;    // taille RÉELLE, même si tronqué
  uint64 total_stderr_bytes = 7;
  repeated string violations = 8;   // tentatives de sortie de périmètre
}

message Frame {
  oneof body {
    Hello hello = 1;
    HelloAck hello_ack = 2;
    RunRequest run = 3;
    RunChunk chunk = 4;
    RunExit exit = 5;
    Cancel cancel = 6;
    Error error = 7;
  }
}
```

`violations` est le champ le plus important du protocole : quand Landlock refuse une écriture ou que le filtre réseau bloque une connexion, le sidecar **le dit**. C'est ce qui transforme un sandbox en instrument de diagnostic au lieu d'un mur muet, et c'est ce qui alimente le rapport de sécurité côté Go.

### 5.3 Les cinq invariants du protocole

1. **Fermé par défaut.** `writable_paths` vide ⇒ `work_dir` seul en écriture. `readable_paths` vide ⇒ lecture du système (binaires, libs) mais jamais des secrets utilisateur déclarés (`~/.ssh`, `~/.aws`, `~/.config/seshat`).
2. **Chemins résolus deux fois.** Le Go résout et normalise, le Rust re-résout après `chdir` et refuse tout `..` résiduel, tout symlink sortant, tout point de reparse Windows.
3. **Un run_id, un processus.** Le sidecar n'accepte jamais deux `RunRequest` avec le même `run_id` ; un `Cancel` tue l'arbre de processus entier.
4. **Troncature annoncée, jamais silencieuse.** `truncated=true` + taille réelle, pour que le modèle sache qu'il ne voit qu'une partie (l'erreur classique qui fait perdre des heures à un agent).
5. **Pas d'héritage d'environnement.** Le sidecar part d'un environnement construit explicitement ; rien du parent ne fuit (`PATH` reconstruit, toutes les variables `*_KEY`, `*_TOKEN`, `AWS_*` supprimées sauf demande explicite).

---

## 6. Correction nécessaire du contrat public

`pkg/grpc/proto/seshat.proto` déclare lui-même, en en-tête, être **« expérimental et partiel »** : les demandes de permission, les questions à l'utilisateur, les plans et les sessions **ne passent pas par ce contrat**. Un seul service, 9 RPC, 224 lignes.

Ce n'est pas bloquant pour la phase A (le sidecar n'utilise pas ce contrat), mais ça l'est pour la suite : si un jour le cœur passe en Rust, c'est ce contrat qui portera la bascule. **Tâche 3.2** : compléter le proto avec les flux de permission et de session *avant* que quiconque en dépende. Tant que ce n'est pas fait, le proto reste une façade pour clients distants, pas une frontière interne.

---

## 7. Les phases

Hypothèse de cadrage : un développeur à temps partiel sérieux (≈20 h/semaine) assisté par agent, sur un dépôt dont il connaît le code. Si tu es à plein temps, divise les durées par ~1,6.

### Phase 0 — Fondations et spike (1 à 2 semaines)

| # | Tâche | Sortie |
|---|---|---|
| 0.1 | `rust/` workspace, `rust-toolchain.toml` épinglé, `cargo fmt`/`clippy` en CI | `cargo build` vert sur Windows + Linux |
| 0.2 | **Spike de consommation codex** : les 3 crates compilent-elles en dépendance git au `rev` épinglé ? | Go/no-go explicite, sinon bascule vendor |
| 0.3 | `proto/sandboxd.proto` + génération (`prost-build` côté Rust, `protoc-gen-go` côté Go) | Les deux côtés génèrent, CI vérifie la dérive |
| 0.4 | Go : `EnvironmentSandboxed`, `SandboxdExecutor` squelette, handshake, `case` dans `NewExecutor` | `go test ./internal/sandbox/...` vert avec un faux sidecar |
| 0.5 | ADR-001 « frontière Go/Rust », ADR-002 « codex comme pièce détachée », ADR-003 « échec fermé » | Décisions écrites, pas implicites |

**Critère de sortie :** un `Run` de `echo hello` traverse Go → sidecar de test (Rust) → Go et rend `hello`, code 0. Rien d'autre. C'est petit, et c'est exactement ce qu'on veut prouver d'abord : que la couture tient.

**Piège à éviter en 0.2 :** ne pas tenter de compiler tout `codex-rs`. On compile **uniquement** les trois crates cibles. Si la compilation tire `codex-core` (482 k lignes), c'est le signal qu'il faut vendorer.

### Phase 1 — Le sandbox réellement utile (3 à 4 semaines)

| # | Tâche | Détail |
|---|---|---|
| 1.1 | Cycle de vie du sidecar | Démarrage paresseux, arrêt à la fermeture de session, reaping des orphelins, `Close` idempotent |
| 1.2 | Framing + streaming | `RunChunk` en continu, backpressure, troncature head/tail avec annonce |
| 1.3 | Probe de capacités | Version de noyau, ABI Landlock, présence de `user_namespaces`, `bwrap`, Job Objects |
| 1.4 | Backend Linux | Landlock (fs) + seccomp (syscalls) + bwrap (namespaces), ou codex si le spike passe |
| 1.5 | Backend Windows | Job Object (mémoire, CPU, processus, kill-on-close) + jeton restreint + ACL de refus d'écriture. **Sans élévation.** |
| 1.6 | Niveau honnête | Si une capacité manque ⇒ `DEGRADED` + `degraded_reasons`. Jamais de repli silencieux. |
| 1.7 | `seshat doctor` | Matrice de capacités par OS, lisible par l'utilisateur |
| 1.8 | Tests | Voir critères ci-dessous |

**Critères d'acceptation (tous doivent passer, sur les deux OS) :**

- écrire hors de `work_dir` échoue, avec une violation rapportée ;
- lire `~/.ssh` échoue ;
- une connexion réseau sortante échoue quand `allow_network=false` ;
- `sleep 300` avec `timeout_ms=1000` rend `timed_out=true` en ≤ 1,5 s et ne laisse **aucun** processus ;
- `yes` avec `max_output_bytes=64k` rend `truncated=true` et la taille réelle ;
- tuer le processus Go ne laisse aucun `seshat-sandboxd` (test automatisé, pas manuel) ;
- un sidecar qui échoue au handshake fait **échouer** l'exécution (pas de repli hôte) ;
- les 104 outils existants passent leur suite **sans modification**.

**Note macOS :** reporté en phase 4. `sandbox-exec` est déprécié ; le chemin propre est `sandbox_init` + profils Seatbelt, non couvert par codex de façon portable. Tant que ton développement est sous Windows, ce n'est pas bloquant — on documente honnêtement `NONE` sur macOS plutôt que de faire semblant.

### Phase 2 — Le moteur de politique (2 à 3 semaines)

| # | Tâche | Détail |
|---|---|---|
| 2.1 | `rust/policy` | `Decision{Allow,Prompt,Forbidden}`, sans branche `default` |
| 2.2 | Langage de règles | Règles de préfixe d'argv, règles réseau (hôte/port/protocole), règles de chemin |
| 2.3 | Chargement | `*.rules` du projet + utilisateur, **échec fermé** sur erreur de parse (avec position) |
| 2.4 | Câblage | Go évalue la politique **avant** de lancer (message clair à l'utilisateur), le sidecar **réapplique** (défense en profondeur) |
| 2.5 | Régression du bug | Test qui reproduit le scénario « auto-mode + aucun classifieur configuré » et exige `Forbidden`, pas `Allow` |
| 2.6 | Propriétés | Test exhaustif : pour toute politique sans règle explicite, aucune commande d'écriture/exécution n'obtient `Allow` |

**Critère de sortie :** la suite de tests contient un test nommé d'après le bug historique, et il passe. On ne corrige pas une classe de bug en la commentant.

### Phase 3 — Consolidation et livraison (2 à 3 semaines)

| # | Tâche | Détail |
|---|---|---|
| 3.1 | Release unifiée | Cross-compilation Go × Rust, binaires par plateforme, somme de contrôle, `NOTICE` codex inclus |
| 3.2 | Compléter `seshat.proto` | Permissions, sessions, plans — sortir du statut « partiel » |
| 3.3 | Packaging | Sidecar embarqué (`go:embed`) ou à côté du binaire, avec résolution de chemin et message d'erreur utile s'il manque |
| 3.4 | Docs | `docs/sandbox.md` : matrice de capacités, comment lire une violation, comment désactiver (et pourquoi c'est déconseillé) |
| 3.5 | Télémétrie | Compteur de violations par outil — la seule métrique qui dit si le sandbox sert |

**Total jusqu'à « le Rust porte une charge réelle » : 8 à 12 semaines.**

### Phase 4 — Portes de décision (pas de travail planifié)

À ce stade on mesure, on ne continue pas par inertie :

| Mesure | Seuil pour continuer |
|---|---|
| Le sidecar est utilisé par défaut chez au moins un utilisateur réel | oui |
| Des violations ont été détectées et rapportées | ≥ 1 cas réel |
| Des bugs d'exécution ont été évités grâce à lui | ≥ 1 cas documenté |
| Le coût de maintenance (rebuild, mises à jour upstream) | < 2 h/mois |

Si un seuil n'est pas atteint : **on supprime le composant**. C'est la règle d'abandon annoncée, écrite ici pour ne pas être renégociée plus tard.

Si les seuils sont atteints, les candidats suivants sont, dans l'ordre : `apply-patch` en Rust (les échecs d'application de patch sont un point de douleur documenté), puis... rien. La TUI et les providers restent en Go.

---

## 8. Risques

| # | Risque | Probabilité | Impact | Mitigation |
|---|---|---|---|---|
| R1 | `cargo` tire `codex-core` par transitivité | Moyenne | Élevé | Spike 0.2 = go/no-go ; repli vendor décidé d'avance |
| R2 | Landlock indisponible sur le noyau cible | Moyenne | Moyen | `DEGRADED` honnête + seccomp/bwrap en repli |
| R3 | Windows exige une élévation (comme codex) | Élevée | Moyen | Phase A sans élévation : Job Objects + jeton restreint seulement |
| R4 | Lenteur de démarrage du sidecar par session | Moyenne | Faible | Démarrage paresseux + pool par session ; mesurer avant d'optimiser |
| R5 | Divergence des deux chaînes de build | Élevée | Moyen | Un seul workflow de release, un seul numéro de version, CI bloquante |
| R6 | Le sidecar grossit jusqu'à devenir un second runtime | Moyenne | **Fatal** | Règle d'or en §1 + revue explicite à chaque PR + porte de phase 4 |
| R7 | Les 104 outils dépendent de subtilités de l'exécution hôte | Moyenne | Élevé | Suite existante doit passer sans modification (critère 1.8) |
| R8 | Le protocole devient un contrat public de fait | Faible | Moyen | Versionné, non documenté publiquement, préfixe `internal` |

R6 est le seul risque fatal, et c'est celui qu'aucun test n'attrape. Il se traite par la gouvernance, pas par la technique.

---

## 9. On commence

### Les trois premiers commits

**Commit 1 — `chore(rust): bootstrap cargo workspace`**
`rust/Cargo.toml`, `rust/rust-toolchain.toml`, `rust/sandboxd/` avec un `main.rs` qui lit une frame sur stdin et répond `HelloAck`, `.gitignore`, job CI Rust (fmt + clippy + build Linux/Windows).

**Commit 2 — `feat(proto): define sandboxd wire protocol`**
`proto/sandboxd.proto` complet, génération `prost` côté Rust, génération Go, cible `make proto` / `just proto`, CI qui échoue si le code généré dérive du `.proto`.

**Commit 3 — `feat(sandbox): add sandboxd executor backend`**
`EnvironmentSandboxed`, `SandboxdConfig`, `SandboxdExecutor` (handshake + `Run` + `Healthy` + `Close`), `case` dans `NewExecutor`, tests avec un sidecar factice qui répond `echo`. Aucun autre fichier touché.

Après ces trois commits, `go test ./...` doit être vert et `seshat` doit fonctionner exactement comme avant, sidecar non utilisé. **C'est le critère qui prouve qu'on a ajouté un chemin, pas remplacé un chemin.**

### Le premier vrai test de valeur

Quatrième étape, non planifiée en tâche mais décisive : brancher `--sandbox=on` sur ta propre utilisation de `seshat` pendant une semaine, et compter les violations rapportées. Si le compteur reste à zéro pendant une semaine d'usage réel, la question n'est pas « comment améliorer le sandbox » mais « pourquoi on l'a construit ».

---

## 10. Ce qu'on ne fait pas

Pour éviter les dérives classiques, liste explicite de refus :

- ❌ Forker `codex` et y greffer Seshat. 122 crates, 2,19 M de lignes, un rythme upstream qui rendrait le rebasage impossible.
- ❌ Réécrire la TUI en Rust. Elle marche, elle est dérivée de Crush, et 473 k lignes côté codex montrent le coût réel.
- ❌ Porter les providers. Les quirks sont déjà payés en Go.
- ❌ Remplacer `modernc` par un binding Rust SQLite. C'est du CGO déguisé.
- ❌ Exposer `seshat-sandboxd` comme serveur MCP. Un sandbox que le modèle peut contourner n'est pas un sandbox : c'est un outil de plus.
- ❌ Écouter sur un port. Même en localhost. Même avec un token.
- ❌ Rendre le sandbox optionnel par défaut. Le défaut actuel (`NoopExecutor`) est précisément le problème qu'on corrige.
- ❌ Ouvrir `seshat-runtime-rs` avant la porte de phase 4.

---

## 11. Résumé exécutable

```
Go        ████████████████████████████████████████░   tout sauf l'exécution
Rust      ████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░   sandbox + politique
codex     12,8 k + 3 k + ~0 lignes consommées        3 crates sur 122
Nouveau   ~350 l. Go, ~4-6 k l. Rust                 8-12 semaines
```

Le pari est délibérément petit. Un bac à sable irréfutable, une politique qui ne peut pas autoriser par accident, un backend de plus dans une interface qui existait déjà. Si ça marche, ça vaut plus que 90 000 lignes de Rust qui refont ce que le Go fait déjà bien.
