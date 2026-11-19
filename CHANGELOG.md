# Changelog

What an operator sees change when they upgrade this module, newest release
first. `version:` in `module.yaml` is the newest heading below.

Write the entry in the same change as the code. One bullet per behaviour a
person outside this repository can observe: a route, a setting, a screen, a
refusal. Name the route and the setting key, never a function or a file.

## 0.1.14

- La barre latérale et l’onglet imprimante affichent « Print Queue ».

## 0.1.13

- La file, ses messages d’erreur et la frise suivent la langue et les formats de Prusa Connect Local, en français ou en anglais.

## 0.1.12

- L’impression en cours et sa progression apparaissent dans la file, y compris si elle a été lancée depuis Prusa Connect ou directement sur l’imprimante.
- La frise décale les travaux en attente selon l’heure de fin estimée de cette impression.

## 0.1.11

- La file respecte les droits du compte sur chaque imprimante. Un administrateur choisit explicitement les imprimantes que l’extension peut opérer.
- Le lancement différé revérifie les droits du compte qui a mis le fichier en file.
- Le suivi utilise les événements de transfert et d’impression de Prusa Connect. Un résultat incertain suspend la file de l’imprimante.
- Une modification est prise en compte seulement après son enregistrement sur disque.

## 0.1.0 - unreleased

### Added

- `GET /print-queue` answers one line and the principal the host authorized.
- The setting `message` sets the line that route answers with.
- File d attente: a page of its own, with an entry in the sidebar.
- File d attente: a tab of its own on each printer's page.
- File d attente: an item in the action menu of each file in storage.
