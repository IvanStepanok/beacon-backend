# Seed photo attribution

Demo evidence photos embedded into the seeder (`go:embed photos/*.jpg`) and
written to `PHOTO_DIR` at seed time so seeded reports serve a real image from
`GET /api/v1/reports/{id}/photo`. All are ground-level (street-level)
photographs of building damage from the February 2023 Kahramanmaraş (Türkiye)
earthquakes in Hatay Province, sourced from Wikimedia Commons under free
licenses — the per-file perspective is stated below. Two earlier files
(`antakya-city-center.jpg`, `antakya-street.jpg`) were REMOVED: they were DJI
drone aerials, which must not be presented as ground-level citizen evidence.

Each file was downscaled to ≤1200 px JPEG for embedding and had ALL metadata
(EXIF/XMP, including camera and any location tags) stripped — seeded "citizen"
photos must not carry a photographer's device fingerprint; no other edits.

Voice of America (VOA) material is in the **public domain** as a work of the
U.S. federal government. CC BY-SA 4.0 files require attribution (given below)
and share-alike for derivatives of the *image itself*.

| File | Perspective | Author | License | Source |
|---|---|---|---|---|
| `antakya-damaged-building.jpg` | Ground level, street view of a collapsed two-storey building | Yıldız Yazıcıoğlu (VOA) | Public domain | <https://commons.wikimedia.org/wiki/File:A_damaged_building_in_Antakya.jpg> |
| `dogu-akdeniz-hospital.jpg` | Ground level, street view of the damaged hospital block | Fatma Yörür (VOA) | Public domain | <https://commons.wikimedia.org/wiki/File:Heavily_damaged_Do%C4%9Fu_Akdeniz_Hospital_after_the_7.8_magnitude_earthquake_in_Turkiye.jpg> |
| `hatay-block-01.jpg` | Ground level, street scene with pancaked apartment block and rescue crews | Hilmi Hacaloğlu (VOA) | Public domain | <https://commons.wikimedia.org/wiki/File:Hatay_in_the_2023_Gaziantep-Kahramanmara%C5%9F_earthquakes_01.jpg> |
| `hatay-block-02.jpg` | Ground level, close view of a collapsed police building | Hilmi Hacaloğlu (VOA) | Public domain | <https://commons.wikimedia.org/wiki/File:Hatay_in_the_2023_Gaziantep-Kahramanmara%C5%9F_earthquakes_02.jpg> |
| `hatay-block-03.jpg` | Ground level, street view of a leaning/partially collapsed block | Hilmi Hacaloğlu (VOA) | Public domain | <https://commons.wikimedia.org/wiki/File:Hatay_in_the_2023_Gaziantep-Kahramanmara%C5%9F_earthquakes_03.jpg> |
| `hatay-feb6-damage.jpg` | Ground level, close-in rubble of a collapsed historic house | Orhan Erkılıç (VOA) | Public domain | <https://commons.wikimedia.org/wiki/File:Damage_in_Hatay_caused_by_the_earthquakes_in_T%C3%BCrkiye_on_February_6,_2023.jpg> |
| `hatay-province-damage.jpg` | Ground level, street view of damaged low-rise housing and debris | Yıldız Yazıcıoğlu (VOA) | Public domain | <https://commons.wikimedia.org/wiki/File:Damage_of_the_Hatay_Province_after_7.8_Mw_earthquake.jpg> |
| `hatay-renkligil-facade.jpg` | Ground level, facing the stripped lower floors of a damaged building | Fatih Renkligil | CC BY-SA 4.0 | <https://commons.wikimedia.org/wiki/File:After_the_7.8_magnitude_earthquake_Hatay_Province_in_Turkey.jpg> |
| `hatay-renkligil-rubble.jpg` | Ground level, sidewalk view toward collapsed buildings | Fatih Renkligil | CC BY-SA 4.0 | <https://commons.wikimedia.org/wiki/File:Hatay%27da_depremin_y%C4%B1k%C4%B1c%C4%B1l%C4%B1%C4%9F%C4%B1,_2023.jpg> |
| `samandag-collapsed-block.jpg` | Ground level, rescuers standing on a pancaked building's slabs | VOA | Public domain | <https://commons.wikimedia.org/wiki/File:A_building_wreck_in_Samandag_district_,Hatay.jpg> |

CC BY-SA 4.0: <https://creativecommons.org/licenses/by-sa/4.0/>
