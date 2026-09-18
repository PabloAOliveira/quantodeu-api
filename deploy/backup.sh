#!/usr/bin/env bash
# Backup do banco do QuantoDeu. Roda na máquina que hospeda o compose.
#
#   ./deploy/backup.sh                 # grava em ./backups
#   ./deploy/backup.sh /mnt/hd/backups # ou onde você quiser
#
# Restaurar:
#   gunzip -c quantodeu-2026-09-18.sql.gz | docker exec -i quantodeu-db \
#     psql -U quantodeu -d quantodeu
set -euo pipefail

DESTINO="${1:-$(cd "$(dirname "$0")/.." && pwd)/backups}"
DIAS="${BACKUP_RETENTION_DAYS:-14}"
USUARIO="${POSTGRES_USER:-quantodeu}"
BANCO="${POSTGRES_DB:-quantodeu}"
ARQUIVO="$DESTINO/quantodeu-$(date +%Y-%m-%d-%H%M).sql.gz"

mkdir -p "$DESTINO"
# --clean --if-exists deixa o dump pronto para restaurar por cima de um banco
# que já existe, sem precisar dropar na mão.
docker exec quantodeu-db pg_dump -U "$USUARIO" -d "$BANCO" --clean --if-exists \
  | gzip > "$ARQUIVO.parcial"
mv "$ARQUIVO.parcial" "$ARQUIVO"   # só vira definitivo se o dump terminou

find "$DESTINO" -name 'quantodeu-*.sql.gz' -mtime "+$DIAS" -delete
echo "backup: $ARQUIVO ($(du -h "$ARQUIVO" | cut -f1))"
