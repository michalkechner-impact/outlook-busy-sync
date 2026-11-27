# OBS busy sync: wzajemne blokady „Busy” w kalendarzach Outlook

Przepływ Power Automate, który co 30 minut przenosi zajętość między kontem firmowym a kontem u klienta. Każde zajęte spotkanie z jednego kalendarza pojawia się w drugim jako prywatna blokada „Busy”. Przy kilku klientach spotkania każdego z nich trafiają przez kalendarz firmowy do pozostałych.

## Co robi, a czego nie
- Czyta tylko czas i status spotkań. Tematu, uczestników, miejsca ani treści nie pobiera.
- Obejmuje okno od wczoraj do 30 dni naprzód. Przesuwa i usuwa blokady razem ze spotkaniami, obsługuje spotkania cykliczne.
- Pomija spotkania całodniowe, oznaczone jako wolne, odwołane i odrzucone.
- Zmienia tylko blokady, które sam utworzył. Rozpoznaje je po ukrytym znaczniku.

## Wymagania
- Power Automate w ramach licencji Microsoft 365.
- Łącznik Office 365 Outlook musi się zalogować do konta u klienta. Jeśli organizacja klienta zablokuje to logowanie, przepływ nie zadziała. Zapytaj też IT klienta, czy takie połączenie jest dozwolone.
- Blokady trafiają do głównych kalendarzy. Kalendarze dodatkowe nie wpływają na zajętość widoczną dla innych.

## Instalacja (ok. 10 minut)
1. make.powerautomate.com, Moje przepływy, Importuj, Importuj pakiet (starsza wersja).
2. Przekaż i wybierz plik obs-busy-sync.zip.
3. Przy „Calendar A” kliknij „Wybierz podczas importowania”, potem „Utwórz nowy” i zaloguj się kontem firmowym. Wróć do importu, kliknij „Odśwież listę”, zaznacz nowe połączenie i zapisz. To samo przy „Calendar B” kontem u klienta.
4. Importuj. Przepływ „OBS busy sync” pojawi się w Moich przepływach jako wyłączony.
5. Edytuj, krok Settings: ustaw dryRun na true i zapisz. Włącz przepływ. Pierwszy przebieg startuje od razu, niczego nie zapisuje, a w kroku Summary pokazuje, ile blokad utworzy.
6. Jeśli liczby się zgadzają, ustaw dryRun na false i zapisz. Kolejny przebieg wypełni całe okno 30 dni.

## Kilku klientów
- Jedna kopia na klienta, zawsze w parze konto firmowe i konto klienta. Calendar A to za każdym razem to samo konto firmowe.
- Kolejną kopię zaimportuj z opcją „Utwórz jako nowe” i inną nazwą. Zanim ją włączysz, wpisz w kroku Settings pairName, czyli krótką nazwę klienta. Kopia z pustym pairName usuwałaby blokady pierwszej kopii.
- Nie łącz klientów bezpośrednio ze sobą. Zajętość i tak przejdzie przez kalendarz firmowy, a para klient z klientem dałaby podwójne blokady.

## Ustawienia (Edytuj, krok Settings)
| Pole | Domyślnie | Znaczenie |
|---|---|---|
| calendarA, calendarB | puste | nazwa kalendarza (wielkość liter ma znaczenie), puste = główny |
| pairName | puste | przy kilku kopiach: krótka nazwa klienta, tylko litery i cyfry, każda kopia inna (bez względu na wielkość liter) |
| dryRun | false | true = przebieg tylko liczy, nic nie zapisuje |
| removeCliBlocks | false | true = usuwa blokady narzędzia outlook-busy-sync (CLI), do 50 na przebieg |
| daysBack, daysAhead | 1, 30 | okno w dniach |
| blockTitle | Busy | tytuł blokady |
| skipDeclined | true | pomijaj odrzucone zaproszenia |
| maxDeletesPerRun | 50 | najwięcej usunięć w jednym przebiegu |

Częstotliwość zmienisz w wyzwalaczu Recurrence. Przebieg bez zmian to około 42 zapytania, czyli około 2 000 na dobę na kopię przy 30 minutach. Limit dla licencji Microsoft 365 to 6 000 na dobę na osobę, wspólny dla wszystkich jej przepływów. Od 3 kopii ustaw 60 minut.

## Przejście z narzędzia CLI
Ustaw dryRun i removeCliBlocks na true i sprawdź w Summary cliA oraz cliB. Potem ustaw dryRun na false: przepływ zastąpi blokady CLI swoimi. Gdy cliA i cliB spadną do 0, przywróć removeCliBlocks na false i nie uruchamiaj już CLI dla tej pary.

## Gdy przebieg kończy się błędem
- CalendarNotFound: nie ma kalendarza o podanej nazwie. Popraw nazwę albo zostaw pole puste.
- TooManyEvents: ponad 500 spotkań w oknie. Zmniejsz daysAhead.
- SafetyCheck: część zmian pominięto, bo usunięć było za dużo naraz, kalendarz źródłowy był pusty albo odczyt był niepełny. W kroku Summary porównaj view i count, powinny być równe. Jeśli błąd się powtarza, wyłącz przepływ.

## Warto wiedzieć
- Komunikat „Wykryto ograniczanie współbieżności wyzwalacza” zignoruj. Nie wyłączaj kontroli współbieżności, bo równoległe przebiegi tworzyłyby podwójne blokady.
- Przed aktualizacją do nowej wersji wyłącz przepływ. Import z opcją „Aktualizuj” nadpisuje krok Settings, więc przepisz swoje ustawienia i dopiero wtedy włącz.
- Wyłączenie albo usunięcie przepływu zostawia istniejące blokady. Usuń je ręcznie albo poczekaj, aż miną.
- Odrzucone zaproszenia są obsłużone, ale nie zostały przetestowane.
