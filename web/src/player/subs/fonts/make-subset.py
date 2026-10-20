import sys
from fontTools.ttLib import TTCollection, TTFont
from fontTools.varLib import instancer
from fontTools import subset
src='/nix/var/nix/profiles/runtime/share/fonts/opentype/noto-cjk/NotoSansCJK-VF.otf.ttc'
col=TTCollection(src)
face=None
for i,f in enumerate(col.fonts):
    n=f['name'].getDebugName(1); print(i,n)
    if n and n.strip()=='Noto Sans CJK SC': face=i
font=TTFont(src, fontNumber=face)
font=instancer.instantiateVariableFont(font, {'wght':500})
chars=set()
def rng(enc, leads, trails):
    for a in leads:
        for b in trails:
            try: chars.update(bytes([a,b]).decode(enc))
            except Exception: pass
rng('gb2312', range(0xA1,0xF8), range(0xA1,0xFF))          # GB2312 (symbols + 6763 hanzi)
rng('big5', range(0xA1,0xC7), list(range(0x40,0x7F))+list(range(0xA1,0xFF)))  # Big5 symbols + level 1
rng('euc_jp', range(0xA1,0xD0), range(0xA1,0xFF))          # JIS X 0208 symbols, kana, level-1 kanji
rng('euc_kr', range(0xB0,0xC9), range(0xA1,0xFF))          # KS X 1001 hangul
chars.update(chr(c) for c in range(0x20,0x250))            # Latin
chars.update(chr(c) for c in range(0x2000,0x2070))         # punctuation
chars.update(chr(c) for c in range(0x3000,0x3100))         # CJK punctuation, kana
chars.update(chr(c) for c in range(0xFF00,0xFFF0))         # fullwidth forms
cmap=font.getBestCmap()
keep=[ord(c) for c in chars if ord(c) in cmap]
print('chars', len(chars), 'in font', len(keep))
opts=subset.Options(); opts.flavor='woff2'; opts.layout_features=['*']; opts.name_IDs=['*']; opts.notdef_outline=True; opts.hinting=False
ss=subset.Subsetter(opts); ss.populate(unicodes=keep); ss.subset(font)
font.flavor='woff2'
font.save('/tmp/cjkfont/NotoSansCJKsc-Medium-subset.woff2')
