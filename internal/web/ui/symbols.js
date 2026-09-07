// APRS symbol names (APRS Protocol Reference v1.0.1 ch.5 + common additions).
// Sprite cell n = code.charCodeAt(0) - 33; 96 cells per table, 16 per row.
// Cells without a name show only the code in the picker.
window.APRS_SYMBOLS = {
  '/': {
    '!': 'Police / Sheriff', '"': 'Reserved', '#': 'Digipeater', '$': 'Phone', '%': 'DX cluster',
    '&': 'HF gateway', "'": 'Small aircraft', '(': 'Mobile satellite station', ')': 'Wheelchair',
    '*': 'Snowmobile', '+': 'Red Cross', ',': 'Boy Scouts', '-': 'House (VHF)', '.': 'X / unknown',
    '/': 'Red dot', '0': 'Circle 0', '1': 'Circle 1', '2': 'Circle 2', '3': 'Circle 3', '4': 'Circle 4',
    '5': 'Circle 5', '6': 'Circle 6', '7': 'Circle 7', '8': 'Circle 8', '9': 'Circle 9', ':': 'Fire',
    ';': 'Campground / picnic', '<': 'Motorcycle', '=': 'Railroad engine', '>': 'Car', '?': 'File server',
    '@': 'Hurricane future prediction', 'A': 'Aid station', 'B': 'BBS', 'C': 'Canoe', 'D': 'Reserved',
    'E': 'Eyeball / event', 'F': 'Farm vehicle / tractor', 'G': 'Grid square (3x3)', 'H': 'Hotel',
    'I': 'TCP/IP node', 'J': 'Reserved', 'K': 'School', 'L': 'PC user', 'M': 'MacAPRS', 'N': 'NTS station',
    'O': 'Balloon', 'P': 'Police car', 'Q': 'Reserved', 'R': 'Recreational vehicle', 'S': 'Shuttle',
    'T': 'SSTV', 'U': 'Bus', 'V': 'ATV', 'W': 'Weather station', 'X': 'Helicopter', 'Y': 'Yacht / sailboat',
    'Z': 'WinAPRS', '[': 'Person / jogger', '\\': 'Triangle (DF station)', ']': 'Mailbox / PBBS',
    '^': 'Large aircraft', '_': 'Weather station', '`': 'Dish antenna', 'a': 'Ambulance', 'b': 'Bicycle',
    'c': 'Incident command post', 'd': 'Fire station', 'e': 'Horse', 'f': 'Fire truck', 'g': 'Glider',
    'h': 'Hospital', 'i': 'IOTA island', 'j': 'Jeep', 'k': 'Truck', 'l': 'Laptop', 'm': 'Mic-E repeater',
    'n': 'Node', 'o': 'Emergency operations center', 'p': 'Rover / dog', 'q': 'Grid square (2x2)',
    'r': 'Antenna / repeater', 's': 'Ship / power boat', 't': 'Truck stop', 'u': 'Truck (18-wheeler)',
    'v': 'Van', 'w': 'Water station', 'x': 'X-APRS (Unix)', 'y': 'Yagi at QTH', 'z': 'Shelter',
    '{': 'Reserved', '|': 'TNC stream switch', '}': 'Reserved', '~': 'TNC stream switch',
  },
  '\\': {
    '!': 'Emergency', '"': 'Reserved', '#': 'Digipeater (overlay)', '$': 'Bank / ATM', '%': 'Power plant',
    '&': 'Gateway (overlay)', "'": 'Crash site', '(': 'Cloudy', ')': 'Firenet MEO / MODIS', '*': 'Snow',
    '+': 'Church', ',': 'Girl Scouts', '-': 'House (HF)', '.': 'Ambiguous / question', '/': 'Waypoint',
    '0': 'Circle (overlay)', '1': 'Reserved', '2': 'Reserved', '3': 'Reserved', '4': 'Reserved',
    '5': 'Reserved', '6': 'Reserved', '7': 'Reserved', '8': '802.11 / WiFi', '9': 'Gas station',
    ':': 'Hail', ';': 'Park / picnic (overlay)', '<': 'Advisory / gale flag', '=': 'APRStt touchtone',
    '>': 'Car (overlay)', '?': 'Info kiosk', '@': 'Hurricane / tropical storm', 'A': 'Box (overlay)',
    'B': 'Blowing snow', 'C': 'Coast Guard', 'D': 'Drizzle', 'E': 'Smoke', 'F': 'Freezing rain',
    'G': 'Snow shower', 'H': 'Haze', 'I': 'Rain shower', 'J': 'Lightning', 'K': 'Kenwood HT',
    'L': 'Lighthouse', 'M': 'MARS / military', 'N': 'Navigation buoy', 'O': 'Rocket', 'P': 'Parking',
    'Q': 'Earthquake', 'R': 'Restaurant', 'S': 'Satellite', 'T': 'Thunderstorm', 'U': 'Sunny',
    'V': 'VORTAC nav aid', 'W': 'NWS site', 'X': 'Pharmacy', 'Y': 'Radios / devices', 'Z': 'Reserved',
    '[': 'Wall cloud', '\\': 'Reserved', ']': 'Reserved', '^': 'Aircraft (overlay)', '_': 'Weather site (overlay)',
    '`': 'Rain', 'a': 'ARRL / ARES / diamond', 'b': 'Blowing dust', 'c': 'Civil defense / RACES',
    'd': 'DX spot', 'e': 'Sleet', 'f': 'Funnel cloud', 'g': 'Gale flags', 'h': 'Store / HAM store',
    'i': 'Black box (overlay)', 'j': 'Work zone', 'k': 'SUV / 4WD', 'l': 'Area / boundary',
    'm': 'Milepost / value sign', 'n': 'Triangle (overlay)', 'o': 'Small circle', 'p': 'Partly cloudy',
    'q': 'Reserved', 'r': 'Restrooms', 's': 'Ship / boat (overlay)', 't': 'Tornado', 'u': 'Truck (overlay)',
    'v': 'Van (overlay)', 'w': 'Flooding', 'x': 'Wreck / obstruction', 'y': 'Skywarn', 'z': 'Shelter (overlay)',
    '{': 'Fog', '|': 'TNC stream switch', '}': 'Reserved', '~': 'TNC stream switch',
  },
};

// Quick picks shown first in the chooser: the symbols emcomm operators reach for.
window.APRS_SYMBOL_FAVORITES = [
  ['/', 'h'], ['/', 'z'], ['\\', 'z'], ['/', 'o'], ['/', 'c'], ['/', '+'], ['/', 'a'], ['/', 'f'],
  ['/', 'd'], ['/', 'A'], ['/', 'w'], ['/', 'E'], ['/', 'r'], ['\\', 'c'], ['\\', 'a'], ['\\', '!'],
];
