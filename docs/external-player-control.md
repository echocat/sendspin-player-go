# Host-managed Player-Control (Ampanion-Migration)

Das SDK stellt eine backendneutrale lokale Player-Steuerung bereit. Ampanion
bleibt für Targetauswahl, Hardwareadapter, Readback und Appliancepolicy zuständig.
Es gibt keine eingebaute CEC-/Mixer-/Discovery-Anbindung und keinen Software-Fallback.

## Exakte öffentliche API

```go
// Beim bestehenden sendspin.New-Aufruf ergänzen:
config.PlayerControl = &sendspin.PlayerControlConfig{
    QueueCapacity: 16,                 // queued + in-flight, Default 16, max. 256
    RequestTimeout: 2 * time.Second,   // einschließlich Queuezeit, max. 1 Minute
}

// Nach New: ein unveränderliches konkretes Host-Target binden.
binding, err := client.BindPlayerControl(sendspin.PlayerControlTarget{
    SetVolume: func(ctx context.Context, volume int) error {
        // Absolut 0..100 perceived loudness. Darf NICHT implizit entmuten.
        return amp.SetVolume(ctx, volume)
    },
    SetMuted: func(ctx context.Context, muted bool) error {
        return amp.SetMuted(ctx, muted)
    },
    Initial: sendspin.PlayerControlReport{
        Capabilities: sendspin.PlayerControlCapabilities{Volume: true, Mute: true},
        Volume: observedVolume, // *int, nil = unbekannt
        Muted: observedMuted,   // *bool, nil = unbekannt
    },
})

// Separater tatsächlicher Readback oder physischer Knopf: niemals SetVolume!
// Vollständiger Report, kein Patch; ausgelassene Werte werden unbekannt.
binding, err = client.ReportPlayerControl(binding, sendspin.PlayerControlReport{
    Capabilities: sendspin.PlayerControlCapabilities{Volume: true, Mute: true},
    Volume: observedVolume,
    Muted: observedMuted,
})

// Nur lokalen Player anfragen, nicht die Sendspin-Gruppe:
err = client.SetVolume(ctx, 50)
err = client.SetMuted(ctx, true)
// Gruppensteuerung bleibt unverändert client.Command(ctx, sendspin.Command{...}).
```

`amp` ist hier die **Ampanion-eigene Adapterinstanz**, keine vom SDK gelieferte API.
Die exakten SDK-Typen und Methoden sind oben gezeigt. Das vollständige kompilierbare
Fake-Host-Beispiel liegt in `_examples/external-control` (`go run .` dort).
Es demonstriert writable/read-only/unavailable, Live-Targetwechsel, separaten
Readback und nichtterminalen Fehlerzustand, ohne physische Hardware vorzutäuschen.

## Semantik und Migration

* `PlayerControl == nil` ist weiterhin der Default: bestehender Software-Gain
  `(volume/100)^1.5`, kurze Rampe, unabhängiges Mute und persistierte Softwarewerte.
* Ein **nicht-nil** `PlayerControl` deaktiviert Software-Gain/Mute/Rampen vollständig,
  auch bei Volume 0, Mute, alten Credentials, Backendverlust und Timeline-Recovery.
  Leere Konfiguration oder leeres Target bedeutet fixed/no-controls, nicht legacy.
  Ungültige externe Konfiguration liefert einen Fehler, niemals einen Legacy-Client.
* Konfiguration kopiert das SDK; der Modus bleibt für die Clientlebensdauer fest.
  Ein späterer Host-Targetwechsel erfolgt über `BindPlayerControl`, nicht über
  Modifikation von `Config`. Vor einem Targetwechsel gespeicherte Tokens verwerfen.
  Bei einem echten Audio-/Connectorwechsel zuerst ein leeres Target binden, damit
  alte Writes und Controls bereits vor dem Routingwechsel ungültig sind. Erst nach
  vertrauenswürdigem Readback den neuen unveränderlichen Adapter binden. Kann das
  neue Target nicht sicher gebunden werden, bleibt es fixed/no-controls; nicht das
  alte Target als Ersatz an einem neuen Ausgang weiterverwenden.
* Relative-only-Hardware darf keinen absoluten Setter anbieten. Read-only:
  Setter weglassen, Capabilities false setzen und beobachtete Werte berichten.
  Volume und Mute sind unabhängig; nur explizite SetMuted-Adapter sind Mute-Setter.
* `supported_commands` bietet `volume` nur mit absolutem Setter, Capability und
  bekanntem Volume an; `mute` nur mit explizitem Setter, Capability und bekanntem
  Mute. RC1 benötigt **keinen** bekannten Mute-State für Volume bzw. umgekehrt.
  Read-only-Werte werden dennoch berichtet. Unbekannte optionale Werte fehlen auf
  dem Wire; `Snapshot.VolumeKnown`/`MutedKnown` unterscheiden sie von 0/false.
  `set_output_delay` und dessen Persistenz bleiben unverändert.
* Lokale Set-Methoden bestätigen im externen Modus nur die **Queueannahme**.
  Das Snapshot-Feld `PlayerControl.VolumeRequest` bzw. `MuteRequest` enthält die
  letzte akzeptierte Anfrage (ID, Wert, pending/succeeded/failed/canceled, Error).
  `succeeded` bedeutet Setter-Rückkehr ohne Fehler, **keinen bestätigten Zustand**.
  Nur ein separater tatsächlicher Readback darf Observationen verändern.
* **Konservative Fehler-Policy:** Liefert ein tatsächlich aufgerufener Setter einen
  Fehler oder eine Panik, entzieht das SDK beide Schreibfähigkeiten. Queued Writes
  werden explizit wegen Capabilityverlust gecancelt; eine teilweise erfolgreiche
  Hardwareoperation ist nicht auszuschließen. Die zuletzt beobachteten Werte
  bleiben als Read-only-Beobachtungen erhalten, niemals als angefragte Werte ersetzt.
  Ein frischer vollständiger `ReportPlayerControl` kann Controls ohne Reconnect
  wiederherstellen. Beim internen Fehlerentzug bleibt das bisher gültige Targettoken
  verwendbar; erst der Report kann über seinen Rückgabewert ein neues Token liefern.
* Fehler bleiben nichtterminal: Stream, Source-Capture, Clock-Sync, Rollen,
  Availability und unveränderlicher Unity-Gain bleiben unberührt. Der betroffene
  letzte Request bleibt `failed` (bei Context-Cancel `canceled`), nicht durch seinen
  eigenen Capabilityentzug nachträglich umklassifiziert. Die neuesten queued
  Requests können dagegen `canceled` sein. `PlayerControl.Error` hält den Hardwarefehler
  unabhängig von Requestüberschreibungen und verlorenen `player_control_error`-Events
  fest, bis ein neuer Hostreport oder eine neue Targetbindung ihn ersetzt/löscht.
  `PlayerControlReport.Err` bleibt eine lokale Hostdiagnose; der Host gibt die dazu
  tatsächlich angebotenen Fähigkeiten ausdrücklich an. Bei erkanntem Backendverlust
  einen Report mit entzogenen Capabilities und ggf. unbekannten Werten senden.
* Reports sind threadsicher und kopieren Werte. Immer das zurückgegebene Binding
  behalten: Capability- oder Known/Unknown-Wechsel erzeugen eine neue Generation.
  Veraltete Tokens liefern `ErrPlayerControlStale`. Ampanion muss seinen aktuellen
  Binding-Handle synchronisieren und Readback-Ergebnisse dem bei Beginn erfassten
  Token zuordnen; nach Targetwechsel niemals mit einem neuen Token alte Ergebnisse melden.
* Die serialisierte FIFO coalesct keine akzeptierten Writes. Abgesehen von
  expliziter Cancellation, Generationwechsel oder dem beschriebenen Capabilityverlust
  werden akzeptierte Writes nicht verworfen.
  Volle Queue lehnt neue Anfragen mit `ErrPlayerControlOverload` ab. Serverseitige
  Überlast-/Backendfehler erzeugen lokale Events, keine Protokollfehler/Disconnects.
  Caller-Cancel und die Deadline gelten einschließlich Queuezeit. **Vor einem
  Setteraufruf** abgebrochene oder abgelaufene Requests erreichen keine Hardware
  und entziehen keine Fähigkeiten; bereits abgebrochene Requests werden nicht
  angenommen. **Nach Beginn eines Setteraufrufs** gelten Cancel/Timeout konservativ
  als unklarer Hardwareausgang und entziehen Controls, auch wenn der Setter spät
  `nil` zurückgibt. Der Request bleibt bei Cancel `canceled`, bei Timeout `failed`.
  Ein zusätzlich gemeldeter echter Backendfehler/eine Panik wird nicht durch einen
  gleichzeitig abgebrochenen Context verdeckt. Session-Cancellation ist kein neuer
  Hardwarefehler; ein bereits stale gewordenes Setterergebnis kann keine spätere
  Target-/Session-/Capabilitygeneration beeinträchtigen. Target-,
  Capability-/Known-State- und Sessionwechsel/Rekey canceln und invalidieren alte
  queued/in-flight Writes; späte Setterergebnisse überschreiben keinen neuen State.
* Adapter werden nie in Protokoll-/Receive-/Decode-/Audio-/Timingloops oder unter
  SDK-Locks aufgerufen. Pro Client existiert höchstens ein Writeworker, nur während
  Arbeit vorhanden ist. **Adapter müssen Context-Cancel zeitnah respektieren.** Go
  kann fremden Code nicht zwangsweise stoppen. Ein fehlerhafter blockierender
  Adapter belegt den einen Worker; es entstehen keine Ersatzworker/Goroutinenlawinen.
* Start, Reconnect, Aktivierung, Targetwechsel und Reports schreiben **nie**
  Hardware. Keine gespeicherten Softwarewerte auf Hardware pushen. Externe
  Anfragen/Beobachtungen ändern Credentials nicht; Identity, Pairing und
  OutputDelay-Store bleiben bestehen. Es ist kein Store-Migrationsschritt nötig.

Spec geprüft: aktuelle RC1-Webspec und gepinnte Revision
`671a34d408581fc25ea56b3528a4a3f13e3be901`, `roles/player/v1.md`.
Die Regeln zu unabhängiger Volume/Mute-Steuerung, optionalen Statefeldern,
Reportability/Settability und dem Ignorieren nicht angebotener Command-Argumente
stimmen in beiden gelesenen Fassungen überein.
