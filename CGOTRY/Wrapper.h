#ifndef Wrapper_hpp
#define Wrapper_hpp
#include<stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
    struct metaData{
        uint64_t cas;
        const char* id;
        unsigned long expiration;
        unsigned long flags;
        int nru;
        uint64_t byseqno;
        uint64_t revseqno;
        unsigned long locktime;
    };
    
    typedef void* EngineObj;
    typedef void* returnType;
    static EngineObj e;
    EngineObj CreateEngine(int NumberOfIsolates);
    void Compile(char* filename,EngineObj e,const char* code);
    returnType Route(EngineObj e,struct metaData meta,const char* doc,const char* filename);
    int getLength(returnType msg);
    void* GetTypeArray(returnType msg);
    void* GetValue(returnType msg);
    const char* getJSON(returnType msg,int index);
    const char* getString(returnType msg,int index);
    int64_t getInt(returnType msg,int index);
    int getType(returnType msg,int index);
    double getFloat(returnType msg,int index);
    int getBool(returnType msg,int index);
#ifdef __cplusplus
}

#endif
#endif

