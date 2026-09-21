#!/usr/bin/env python3
"""Rebuilds src/data/cities.tsv and src/data/country-numeric.json from GeoNames dumps.

  curl -LO https://download.geonames.org/export/dump/cities15000.zip && unzip cities15000.zip
  curl -LO https://download.geonames.org/export/dump/countryInfo.txt
  python3 scripts/build-geodata.py cities15000.txt countryInfo.txt

GeoNames data is CC BY 4.0 (https://www.geonames.org/). Keep the attribution in the README and the app.
"""
import json, sys

cities_txt, country_txt = sys.argv[1], sys.argv[2]
rows = []
for line in open(cities_txt, encoding='utf-8'):
    f = line.rstrip('\n').split('\t')
    name, ascii_name, lat, lng, cc, pop = f[1], f[2], float(f[4]), float(f[5]), f[8], int(f[14] or 0)
    if not cc or not name:
        continue
    # "name|ascii-if-different|cc|lat|lng|population" — the app folds accents itself for search
    rows.append((pop, f"{name}|{ascii_name if ascii_name != name else ''}|{cc}|{lat:.3f}|{lng:.3f}|{pop}"))
rows.sort(key=lambda r: -r[0])
open('src/data/cities.tsv', 'w', encoding='utf-8').write('\n'.join(r[1] for r in rows) + '\n')

num = {}
for line in open(country_txt, encoding='utf-8'):
    if line.startswith('#') or not line.strip():
        continue
    f = line.split('\t')
    if f[2].isdigit():
        num[str(int(f[2]))] = f[0]
json.dump(num, open('src/data/country-numeric.json', 'w'), separators=(',', ':'), sort_keys=True)
print(len(rows), 'cities;', len(num), 'countries')
