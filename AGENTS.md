# AGENTS.md — t-invest-portfolio

Личная утилита чтения портфеля через T-Invest API. Торговые методы клиент не вызывает. Токен только в окружении.

## Обязательно прочитать

1. [docs/Правила/Индекс_правил.md](docs/Правила/Индекс_правил.md)
2. [docs/Правила/Правила_проекта.md](docs/Правила/Правила_проекта.md)

Краткое правило Cursor: [`.cursor/rules/project-operations.mdc`](.cursor/rules/project-operations.mdc).

Продуктового канона (требования, инварианты, ADR) нет. Снимки в `docs/tinvest/` — справка брокера, не ТЗ.

## Лог

Изменения агента и претензии пользователя фиксировать в [docs/Правила/cursor_log.md](docs/Правила/cursor_log.md). Значение токена туда не писать.

## Код

- `internal/config` — `FromEnv`: `TINVEST_TOKEN`, `TINVEST_ENDPOINT`, `TINVEST_ACCOUNT_ID`. Адрес по умолчанию — боевой контур.
- `internal/tinvest` — `Client`: счета, портфель, инструмент. Без торговых вызовов.
- `internal/portfolio` — модели счётов, позиций и событий опроса.
