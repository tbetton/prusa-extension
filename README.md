# File d'attente (print-queue) — extension Prusa Connect Local

Une file d'impressions par imprimante. Quand l'imprimante passe **READY** (plateau vidé
et confirmé sur l'écran), l'extension envoie le fichier suivant depuis le stockage du
serveur et lance l'impression. Un travail peut être programmé (« pas avant » une heure).

- Page « Print Queue » dans la barre latérale (toutes les imprimantes)
- Onglet « Print Queue » sur la page de chaque imprimante
- Menu d'un fichier du stockage : « Ajouter à la file d'attente »
- Monter / descendre, retirer, programmer, remettre en file, suspendre la file
- File gardée dans `queue.json` (dossier de données du module)

## Construire et installer

Pour installer la version publiée, ouvre **Settings → Extensions** dans
Prusa Connect Local et cherche **Print Queue** dans le Marketplace.

Pour construire depuis les sources (Go 1.26.11, Node 24 et pnpm 11.3.0) :

```sh
git clone --depth 1 --branch sdk/v0.1.0-beta.1 https://github.com/prusa3d/prusa-connect.git .core
go test -race ./...
BUILD_ONLY=1 sh install.sh
```

Le SDK officiel est attendu dans `.core/`. `BUILD_ONLY=1 sh install.sh` construit
le manifeste, le binaire de la plateforme courante et le bundle dans `ui/dist`.
`UI_ONLY=1 sh install.sh` construit le bundle seul, au format attendu par le
[builder de Prusa](https://plugin-marketplace.prusa3d.com/docs/connect/github-actions).

Pour vérifier le module, lance `go vet ./...` et `go test -race ./...`.

## Droit de lancer une impression

`-approve` autorise les permissions déclarées par le module. Pour le lancement
automatique, un administrateur accorde explicitement le rôle d’opérateur au module
sur chaque imprimante sélectionnée :

    POST /api/v1/grants/device/<id imprimante>
    {"relation": "operator", "subject": "sink:print-queue"}

La file et l’historique sont visibles selon le droit d’observer chaque
imprimante. L’ajout et la modification d’un travail demandent le droit
d’opérer cette imprimante. Les fichiers du stockage serveur demandent
également le droit de lecture de ce stockage pour l’ajout et le lancement.

Au démarrage d’un travail, Connect vérifie à nouveau les droits de la personne
qui l’a mis ou remis en file. Autorise aussi `check_absent_people` lors de
l’installation pour que Connect puisse vérifier les comptes locaux après un
redémarrage. En mode ouvert ou avec OIDC, Connect peut ne pas pouvoir vérifier
une personne dont il n’a pas traité de requête depuis son démarrage. Le travail
est alors marqué en échec. La personne peut le remettre en file après
avoir rouvert l’extension.

## Suivi des impressions

Le résultat d’une impression vient des événements de Prusa Connect. Pour un
fichier transféré depuis le serveur, le nom sur l’imprimante reçoit un préfixe
unique qui relie le transfert à sa commande de démarrage. Le fichier source
garde son nom.

Quand le résultat d’un lancement est incertain, la file de cette imprimante
est suspendue. Vérifie l’impression en cours et retire la pièce si nécessaire
avant de reprendre la file. Cela s’applique aussi aux travaux déjà en cours
lors d’une mise à jour depuis la version 0.1.10, qui ne stockait pas les
identifiants de suivi.

## Réglages (Settings > Extensions > Print Queue)

| clé | défaut | rôle |
|---|---|---|
| poll_seconds | 10 | fréquence de vérification des imprimantes |
| start_timeout_minutes | 30 | délai max entre l'envoi et le début d'impression |
| printer_folder | usb | dossier de destination sur l'imprimante |
| keep_history | 20 | nombre de travaux terminés gardés par imprimante |


## Publication

Le workflow **Prusa Marketplace** effectue un essai à blanc sur chaque push
sur `main` et à chaque lancement manuel. Une release GitHub publiée avec le tag correspondant à la version du
manifeste (par exemple `v0.1.14`) soumet le build à Prusa. Une préversion GitHub
utilise le canal bêta.

[Guide de publication Prusa](https://plugin-marketplace.prusa3d.com/docs/connect/github-actions)
