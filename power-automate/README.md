# OBS busy sync (Outlook Busy Sync): Twoja zajętość widoczna w obu firmach

Co 30 minut przenosi Twoje spotkania między kalendarzem firmowym a kalendarzem u klienta jako prywatne blokady „Busy”. Nikt nie zobaczy tematów ani uczestników, bo przepływ w ogóle ich nie czyta.

![Jak działa OBS busy sync](obs-busy-sync-diagram.png)

## Zanim zaczniesz (2 min)
- [ ] Klient ma pocztę na Microsoft 365, czyli logujesz się do niej przez outlook.office.com.
- [ ] IT klienta się zgadza. Jeśli nie wiesz, zapytaj.
- [ ] Masz plik `obs-busy-sync.zip` i 15 minut.

## Krok 1. Wgraj paczkę
1. Otwórz make.powerautomate.com, a w nim **Moje przepływy**.
2. Kliknij **Importuj**, potem **Importuj pakiet (starsza wersja)**.
3. Kliknij **Przekaż** i wybierz `obs-busy-sync.zip`.
4. Poczekaj około minuty na komunikat „Twój pakiet został przekazany pomyślnie”.

## Krok 2. Podłącz konta
1. Przy **Calendar A** kliknij **Wybierz podczas importowania**.
2. Kliknij **Utwórz nowy** i zaloguj się **kontem firmowym**.
3. Wróć do karty importu. Kliknij **Odśwież listę**, zaznacz nowe połączenie i zapisz.
4. Zrób to samo przy **Calendar B**, ale **kontem u klienta**.
5. Kliknij **Importuj**. Przepływ „OBS busy sync” pojawi się w Moich przepływach jako wyłączony.

## Krok 3. Próba na sucho
1. Otwórz przepływ, kliknij **Edytuj**, potem krok **Settings**.
2. Zmień `"dryRun": false` na `"dryRun": true` i zapisz.
3. Wróć do przepływu i **włącz** go. Pierwszy przebieg rusza od razu i niczego nie zapisuje.
4. Po 1-2 minutach otwórz **Historię przebiegów**, potem ostatni przebieg i krok **Summary**.
5. Sprawdź dwie rzeczy:
   - `viewA` = `countA` i `viewB` = `countB`;
   - `AtoB.create` (blokady u klienta) i `BtoA.create` (blokady w firmie) mniej więcej odpowiadają liczbie Twoich spotkań w ciągu 30 dni.

## Krok 4. Na serio
1. Otwórz **Edytuj**, potem **Settings**, ustaw `"dryRun": false` i zapisz.
2. Najpóźniej po 30 minutach zobaczysz blokady „Busy” w obu kalendarzach.

**Działa, gdy** kolejny przebieg pokazuje w Summary `create`, `update` i `delete` równe 0 w obu kierunkach.

## Masz drugiego klienta?
- Zaimportuj ten sam zip jeszcze raz. Wybierz **Utwórz jako nowe** i nadaj inną nazwę.
- Calendar A to to samo konto firmowe, Calendar B to nowy klient.
- Zanim włączysz, wpisz w Settings `"pairName"`, np. `"acme"`. Tylko litery i cyfry, każda kopia inna.
- Od 3 kopii zmień w wyzwalaczu **Recurrence** 30 minut na 60, żeby nie przekroczyć dobowego limitu zapytań.

## Nie rób tego
- **Nie wyłączaj kontroli współbieżności,** nawet jeśli Power Automate to proponuje. Powstałyby podwójne blokady.
- **Nie łącz dwóch klientów bezpośrednio.** Zajętość i tak przejdzie przez kalendarz firmowy.
- **Nie aktualizuj włączonego przepływu.** Wyłącz go, zaimportuj z opcją **Aktualizuj**, przepisz swoje Settings i dopiero włącz.

## Coś nie działa
| Błąd | Co zrobić |
|---|---|
| CalendarNotFound | Popraw nazwę w `calendarA` / `calendarB` albo zostaw puste |
| TooManyEvents | Zmniejsz `daysAhead` w Settings |
| SafetyCheck | Sprawdź w Summary, czy `viewA` = `countA` i `viewB` = `countB`. Jeśli błąd się powtarza, wyłącz przepływ i daj znać |
| Nie da się utworzyć połączenia B | Organizacja klienta blokuje Power Automate. Po Twojej stronie nic się z tym nie da zrobić |

## Wyłączenie
Wyłącz przepływ. Blokady zostaną w kalendarzach: usuń je ręcznie albo poczekaj, aż miną.
