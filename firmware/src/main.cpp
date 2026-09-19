#include <Arduino.h>
#include <EEPROM.h>
#include "SliderLEDControl.h"

// ---------- UPLINK ----------
// Frame format "v1|v2|v3|v4|v5\n" at 115200 baud (deej-style). Unlike deej
// we only send when a slider actually moved (>= SEND_DELTA raw units), at
// most every SEND_MIN_MS, plus a keepalive frame every SEND_KEEPALIVE_MS so
// the PC can tell the link is alive and re-sync after a reconnect. That
// cuts idle traffic from 100 lines/s to 2 lines/s.
const int NUM_SLIDERS = 5;
int analogSliderValues[NUM_SLIDERS];
int lastSentValues[NUM_SLIDERS] = {-1, -1, -1, -1, -1};
unsigned long lastSent = 0;
const unsigned long SEND_MIN_MS = 20;        // 50 Hz cap while moving
const unsigned long SEND_KEEPALIVE_MS = 500; // idle heartbeat
const int SEND_DELTA = 2;                    // raw ADC units

// Set to true to print labeled per-pin values for wiring debug.
// WARNING: breaks the deej protocol — disable before using with deej.
#define DEBUG_PRINT_SLIDERS false

// ---------- THEME + BRIGHTNESS STATE ----------
int currentTheme = 0;
int brightness = 10;
const int maxBrightness = 50;

// ESP32 DevKit V1 pin mapping. Sliders on ADC1 pins only (ADC2 conflicts
// with WiFi). LED data pins avoid boot-strap pins (0, 2, 12, 15) and
// flash pins (6-11).
SliderLEDControl sliders[NUM_SLIDERS] = {
    SliderLEDControl(32, 25, false),
    SliderLEDControl(33, 26, false),
    SliderLEDControl(34, 27, false),
    SliderLEDControl(35, 14, false),
    SliderLEDControl(36, 13, false)
};

const char* themes[7][5][3] = {
    // Theme 0: universal blue/violet/red
    {
        {"#0095ff", "#9900ff", "#ff0000"},
        {"#0095ff", "#9900ff", "#ff0000"},
        {"#0095ff", "#9900ff", "#ff0000"},
        {"#0095ff", "#9900ff", "#ff0000"},
        {"#0095ff", "#9900ff", "#ff0000"}
    },
    // Theme 1: per-slider semantic (master / browser / discord / spotify / game)
    {
        {"#0095ff", "#9900ff", "#ff0000"},
        {"#0095ff", "#9900ff", "#ff0000"},
        {"#ffffff", "#5865f2", "#2839f9"},
        {"#ffffff", "#1ed760", "#007229"},
        {"#009600", "#ffff00", "#ff0000"}
    },
    // Theme 2: all red
    {
        {"#FF0000", "#FF0000", "#FF0000"}, {"#FF0000", "#FF0000", "#FF0000"},
        {"#FF0000", "#FF0000", "#FF0000"}, {"#FF0000", "#FF0000", "#FF0000"},
        {"#FF0000", "#FF0000", "#FF0000"}
    },
    // Theme 3: all white
    {
        {"#FFFFFF", "#FFFFFF", "#FFFFFF"}, {"#FFFFFF", "#FFFFFF", "#FFFFFF"},
        {"#FFFFFF", "#FFFFFF", "#FFFFFF"}, {"#FFFFFF", "#FFFFFF", "#FFFFFF"},
        {"#FFFFFF", "#FFFFFF", "#FFFFFF"}
    },
    // Theme 4: all blue
    {
        {"#0066ff", "#0066ff", "#0066ff"}, {"#0066ff", "#0066ff", "#0066ff"},
        {"#0066ff", "#0066ff", "#0066ff"}, {"#0066ff", "#0066ff", "#0066ff"},
        {"#0066ff", "#0066ff", "#0066ff"}
    },
    // Theme 5: classic VU (green/yellow/red)
    {
        {"#00ff0d", "#ffe600", "#ff0000"}, {"#00ff0d", "#ffe600", "#ff0000"},
        {"#00ff0d", "#ffe600", "#ff0000"}, {"#00ff0d", "#ffe600", "#ff0000"},
        {"#00ff0d", "#ffe600", "#ff0000"}
    },
    // Theme 6: pink
    {
        {"#00ffff", "#ff54f9", "#ffa6fc"}, {"#00ffff", "#ff54f9", "#ffa6fc"},
        {"#00ffff", "#ff54f9", "#ffa6fc"}, {"#00ffff", "#ff54f9", "#ffa6fc"},
        {"#00ffff", "#ff54f9", "#ffa6fc"}
    }
};
const int NUM_THEMES = sizeof(themes) / sizeof(themes[0]);

// Per-slider override flag. When true, that slider ignores the active
// theme and shows the colors set via "O:i,r,g,b" from the PC.
bool sliderOverridden[NUM_SLIDERS] = {false, false, false, false, false};

// LED rendering modes selectable from the PC (MODE:<n> downlink command).
enum LedMode {
    LED_MODE_POSITION = 0, // bars follow slider physical position
    LED_MODE_RAINBOW  = 1, // animated HSV rotation, ignores slider value
    LED_MODE_METER    = 2, // bars driven by PC-supplied audio peak meters
};
int ledMode = LED_MODE_POSITION;
int meterValues[NUM_SLIDERS] = {0, 0, 0, 0, 0};
uint16_t rainbowPhase = 0;

// ---------- HEX → RGB ----------
int getRgbComponent(const char* hex, char component) {
    if (hex[0] != '#' || strlen(hex) != 7) return -1;
    char compStr[3] = {0};
    int offset = (component == 'R' || component == 'r') ? 1
               : (component == 'G' || component == 'g') ? 3
               : (component == 'B' || component == 'b') ? 5 : -1;
    if (offset < 0) return -1;
    strncpy(compStr, hex + offset, 2);
    return strtol(compStr, NULL, 16);
}

// ---------- EEPROM HELPERS ----------
// Layout: address 0..1 — currentTheme, 2..3 — brightness.
void writeIntIntoEEPROM(int address, int number) {
    EEPROM.write(address, number >> 8);
    EEPROM.write(address + 1, number & 0xFF);
    EEPROM.commit();
}

int readIntFromEEPROM(int address) {
    return (EEPROM.read(address) << 8) + EEPROM.read(address + 1);
}

// ---------- APPLY STATE ----------
void applyThemeToSlider(int i) {
    if (sliderOverridden[i]) return;
    const char** sections = themes[currentTheme][i];
    sliders[i].setColors(
        getRgbComponent(sections[0], 'R'),
        getRgbComponent(sections[0], 'G'),
        getRgbComponent(sections[0], 'B'),
        getRgbComponent(sections[1], 'R'),
        getRgbComponent(sections[1], 'G'),
        getRgbComponent(sections[1], 'B'),
        getRgbComponent(sections[2], 'R'),
        getRgbComponent(sections[2], 'G'),
        getRgbComponent(sections[2], 'B')
    );
}

void setBrightness(int newValue) {
    if (newValue > maxBrightness) newValue = maxBrightness;
    else if (newValue < 0) newValue = 0;

    brightness = newValue;
    writeIntIntoEEPROM(2, newValue);
    for (int i = 0; i < NUM_SLIDERS; i++) {
        sliders[i].setBrightness(newValue);
    }
}

void setTheme(int theme) {
    if (theme < 0) theme = NUM_THEMES - 1;
    else if (theme >= NUM_THEMES) theme = 0;

    currentTheme = theme;
    writeIntIntoEEPROM(0, currentTheme);

    for (int i = 0; i < NUM_SLIDERS; i++) {
        applyThemeToSlider(i);
    }
}

// ---------- UPLINK ----------
void printAllAnalogPins() {
    const uint8_t pins[] = {32, 33, 34, 35, 36, 39};
    const int n = sizeof(pins) / sizeof(pins[0]);
    for (int i = 0; i < n; i++) {
        Serial.print("GPIO");
        Serial.print(pins[i]);
        Serial.print(": ");
        Serial.print(analogRead(pins[i]));
        if (i < n - 1) Serial.print("  ");
    }
    Serial.println();
}

void sendSliderValues() {
    if (DEBUG_PRINT_SLIDERS) {
        printAllAnalogPins();
        return;
    }
    // Fixed buffer instead of String concatenation: no heap churn.
    char out[NUM_SLIDERS * 5 + 2];
    int n = 0;
    for (int i = 0; i < NUM_SLIDERS; i++) {
        n += snprintf(out + n, sizeof(out) - n, i ? "|%d" : "%d", analogSliderValues[i]);
        lastSentValues[i] = analogSliderValues[i];
    }
    Serial.println(out);
}

// True if any slider moved enough since the last transmitted frame.
bool slidersChanged() {
    for (int i = 0; i < NUM_SLIDERS; i++) {
        int d = analogSliderValues[i] - lastSentValues[i];
        if (d >= SEND_DELTA || d <= -SEND_DELTA) return true;
    }
    return false;
}

// "STATE:<theme>,<brightness>,<mode>" — sent on GET and whenever the
// device changes one of these on its own (slider gestures), so the PC GUI
// always shows what the hardware is really doing.
void sendState() {
    Serial.print("STATE:");
    Serial.print(currentTheme);
    Serial.print(',');
    Serial.print(brightness);
    Serial.print(',');
    Serial.println(ledMode);
}

// ---------- DOWNLINK (PC → ESP32 commands) ----------
// Protocol (text, one command per line, terminated with '\n'):
//   T:<n>             — set theme n (0..NUM_THEMES-1), persisted in EEPROM
//   B:<n>             — set brightness 0..maxBrightness, persisted in EEPROM
//   O:<i>,<r>,<g>,<b> — override slider i (0..4) with solid color rgb
//   R                 — clear all overrides, repaint current theme
//   MODE:<n>          — 0=position, 1=rainbow, 2=meter; persisted
//   M:<v1>,…,<v5>     — peak meter values 0..1023 for meter mode
//   GET               — reply "STATE:<theme>,<brightness>,<mode>"
//   PING              — reply "PONG" (used by PC GUI to identify our port)
// Unknown lines are silently ignored to keep the uplink readable.

String cmdBuf;

void overrideSlider(int i, int r, int g, int b) {
    if (i < 0 || i >= NUM_SLIDERS) return;
    sliderOverridden[i] = true;
    sliders[i].setColors(r, g, b, r, g, b, r, g, b);
}

void clearOverrides() {
    for (int i = 0; i < NUM_SLIDERS; i++) {
        sliderOverridden[i] = false;
        applyThemeToSlider(i);
    }
}

void setLedMode(int mode) {
    if (mode < 0 || mode > LED_MODE_METER) return;
    ledMode = mode;
    EEPROM.write(4, (uint8_t)mode);
    EEPROM.commit();
}

void parseMeter(const String& csv) {
    int idx = 0, start = 0;
    int len = csv.length();
    for (int i = 0; i <= len; i++) {
        if (i == len || csv[i] == ',') {
            if (idx < NUM_SLIDERS) {
                int v = csv.substring(start, i).toInt();
                if (v < 0) v = 0;
                if (v > 1023) v = 1023;
                meterValues[idx++] = v;
            }
            start = i + 1;
        }
    }
}

void handleCommand(const String& line) {
    if (line.length() == 0) return;

    if (line == "PING") {
        Serial.println("PONG");
        return;
    }
    if (line == "GET") {
        sendState();
        return;
    }
    if (line == "R") {
        clearOverrides();
        return;
    }
    if (line.startsWith("T:")) {
        setTheme(line.substring(2).toInt());
        sendState();
        return;
    }
    if (line.startsWith("B:")) {
        setBrightness(line.substring(2).toInt());
        sendState();
        return;
    }
    if (line.startsWith("O:")) {
        // O:i,r,g,b
        int p1 = line.indexOf(',', 2);
        int p2 = line.indexOf(',', p1 + 1);
        int p3 = line.indexOf(',', p2 + 1);
        if (p1 < 0 || p2 < 0 || p3 < 0) return;
        int i = line.substring(2, p1).toInt();
        int r = line.substring(p1 + 1, p2).toInt();
        int g = line.substring(p2 + 1, p3).toInt();
        int b = line.substring(p3 + 1).toInt();
        overrideSlider(i, r, g, b);
        return;
    }
    if (line.startsWith("MODE:")) {
        setLedMode(line.substring(5).toInt());
        sendState();
        return;
    }
    if (line.startsWith("M:")) {
        parseMeter(line.substring(2));
        return;
    }
}

void readSerialCommands() {
    while (Serial.available()) {
        char c = (char)Serial.read();
        if (c == '\r') continue;
        if (c == '\n') {
            handleCommand(cmdBuf);
            cmdBuf = "";
        } else if (cmdBuf.length() < 64) {
            cmdBuf += c;
        }
    }
}

// ---------- SETUP / LOOP ----------
void setup() {
    Serial.begin(115200);
    // EEPROM layout: 0..1 theme, 2..3 brightness, 4 ledMode.
    EEPROM.begin(8);

    // ESP32 ADC: 10-bit resolution + 11dB attenuation -> 0..1023 over 0..~3.3V.
    // Keeps SliderLEDControl math (uses 0..1023) and deej protocol unchanged.
    analogReadResolution(10);
    analogSetAttenuation(ADC_11db);

    brightness = readIntFromEEPROM(2);
    if (brightness < 0 || brightness > maxBrightness) {
        brightness = 20;
        writeIntIntoEEPROM(2, brightness);
    }

    currentTheme = readIntFromEEPROM(0);
    if (currentTheme < 0 || currentTheme >= NUM_THEMES) {
        currentTheme = 0;
        writeIntIntoEEPROM(0, currentTheme);
    }

    uint8_t storedMode = EEPROM.read(4);
    if (storedMode > LED_MODE_METER) storedMode = LED_MODE_POSITION;
    ledMode = storedMode;

    setBrightness(brightness);
    setTheme(currentTheme);

    // Opening the COM port on the PC usually resets us (DTR), so announce
    // our state once we're up: the GUI syncs without having to ask.
    sendState();
}

void loop() {
    readSerialCommands();

    // Sample every loop iteration regardless of render mode so the
    // uplink values and the double-tap gestures keep working when the
    // LEDs are showing rainbow/meter visuals instead of position bars.
    for (int i = 0; i < NUM_SLIDERS; i++) {
        sliders[i].sample();
    }

    // Local UI: double-tap-to-zero gestures on sliders 2..5. Each one
    // changes device state, so tell the PC about it.
    if (sliders[1].checkForDoubleZero()) { setBrightness(brightness - 10); sendState(); }
    if (sliders[2].checkForDoubleZero()) { setBrightness(brightness + 10); sendState(); }
    if (sliders[3].checkForDoubleZero()) { setTheme(currentTheme - 1);     sendState(); }
    if (sliders[4].checkForDoubleZero()) { setTheme(currentTheme + 1);     sendState(); }

    switch (ledMode) {
        case LED_MODE_POSITION:
            for (int i = 0; i < NUM_SLIDERS; i++) sliders[i].render();
            break;
        case LED_MODE_RAINBOW:
            // Offset each strip by 1/NUM_SLIDERS of a full rotation so the
            // rainbow appears to move across the device, not in lock-step.
            for (int i = 0; i < NUM_SLIDERS; i++) {
                uint16_t offset = (uint16_t)(i * (65535 / NUM_SLIDERS));
                sliders[i].renderRainbow(rainbowPhase + offset);
            }
            rainbowPhase += 256; // ~256 * 100Hz = full cycle ≈2.5 s
            break;
        case LED_MODE_METER:
            for (int i = 0; i < NUM_SLIDERS; i++) {
                sliders[i].showLights(meterValues[i]);
            }
            break;
    }

    unsigned long now = millis();
    unsigned long sinceSent = now - lastSent;
    if (sinceSent >= SEND_MIN_MS) {
        for (int i = 0; i < NUM_SLIDERS; i++) {
            analogSliderValues[i] = sliders[i].getAverageValue();
        }
        if (slidersChanged() || sinceSent >= SEND_KEEPALIVE_MS) {
            sendSliderValues();
            lastSent = now;
        }
    }
}
